package main

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"net"
	"strconv"
)

// TLS wire constants that the ClientHello parser reads. kevin-relay never
// terminates TLS, so it reads only what it needs to find the server name.
const (
	recordTypeHandshake      = 0x16
	handshakeTypeClientHello = 0x01
	extensionServerName      = 0
	serverNameTypeHostName   = 0
)

// maxClientHello bounds the bytes that handleHTTPS reads to find the
// ClientHello. It covers the largest plaintext TLS record, plus the record
// header.
const maxClientHello = 5 + 16384

// handleHTTPS reads the TLS ClientHello of conn without terminating TLS,
// opens a CONNECT tunnel to proxyAddr for the SNI host on port, and pipes
// the connection through it.
func handleHTTPS(ctx context.Context, conn net.Conn, proxyAddr string, port int) {
	defer func() { _ = conn.Close() }()

	client := bufio.NewReaderSize(conn, maxClientHello)
	hello, err := readClientHello(client)
	if err != nil {
		log.Ctx(ctx).Debug("relay: https: read client hello failed", "error", err)
		return
	}

	sni, err := parseClientHelloSNI(hello)
	if err != nil {
		log.Ctx(ctx).Debug("relay: https: no server name", "error", err)
		return
	}

	upstream, upstreamReader, err := connectProxy(ctx, proxyAddr, sni, port)
	if err != nil {
		log.Ctx(ctx).Debug("relay: https: connect to proxy failed", "error", err, "sni", sni)
		return
	}
	defer func() { _ = upstream.Close() }()

	// The ClientHello bytes are already consumed from conn. Write them to
	// the upstream connection before the pipe starts, or the handshake
	// never reaches the proxy.
	if _, err := upstream.Write(hello); err != nil {
		log.Ctx(ctx).Debug("relay: https: forward client hello failed", "error", err)
		return
	}

	pipe(client, conn, upstreamReader, upstream)
}

// readClientHello reads one TLS record from r and returns its bytes,
// starting at the record header. It reports [ErrNotHandshake] when the
// record is not a handshake record, and [ErrTruncated] when r ends before
// the record is complete.
func readClientHello(r *bufio.Reader) ([]byte, error) {
	header, err := r.Peek(5)
	if err != nil {
		return nil, fmt.Errorf("%w: %w", ErrTruncated, err)
	}

	n, err := recordLength(header)
	if err != nil {
		return nil, err
	}
	total := 5 + n
	if total > maxClientHello {
		return nil, fmt.Errorf("relay: client hello of %d bytes exceeds the limit: %w", total, ErrTruncated)
	}

	raw, err := r.Peek(total)
	if err != nil {
		return nil, fmt.Errorf("%w: %w", ErrTruncated, err)
	}
	hello := make([]byte, total)
	copy(hello, raw)

	if _, err := r.Discard(total); err != nil {
		return nil, fmt.Errorf("relay: discard client hello: %w", err)
	}
	return hello, nil
}

// connectProxy opens a CONNECT tunnel to proxyAddr for host on port. It
// returns the upstream connection and a reader that carries any byte the
// proxy already sent along with the response headers.
func connectProxy(ctx context.Context, proxyAddr, host string, port int) (net.Conn, *bufio.Reader, error) {
	var d net.Dialer
	upstream, err := d.DialContext(ctx, "tcp", proxyAddr)
	if err != nil {
		return nil, nil, fmt.Errorf("relay: dial proxy: %w", err)
	}

	target := host + ":" + strconv.Itoa(port)
	req := "CONNECT " + target + " HTTP/1.1\r\nHost: " + target + "\r\n\r\n"
	if _, err = io.WriteString(upstream, req); err != nil {
		_ = upstream.Close()
		return nil, nil, fmt.Errorf("relay: send connect request: %w", err)
	}

	br := bufio.NewReader(upstream)
	status, err := br.ReadString('\n')
	if err != nil {
		_ = upstream.Close()
		return nil, nil, fmt.Errorf("relay: read connect response: %w", err)
	}
	if !isConnectOK(status) {
		_ = upstream.Close()
		return nil, nil, fmt.Errorf("%w: %q", ErrProxyRejected, status)
	}

	// Discard the rest of the response header block, up to the blank line
	// that ends it.
	for {
		line, err := br.ReadString('\n')
		if err != nil {
			_ = upstream.Close()
			return nil, nil, fmt.Errorf("relay: read connect response: %w", err)
		}
		if line == "\r\n" || line == "\n" {
			break
		}
	}
	return upstream, br, nil
}

// isConnectOK reports whether statusLine, the first line of an HTTP
// response, carries a 200 status.
func isConnectOK(statusLine string) bool {
	return len(statusLine) >= len("HTTP/1.1 200") && statusLine[9:12] == "200"
}

// pipe copies bytes from clientR to upstreamW and from upstreamR to
// clientW, until either direction ends.
func pipe(clientR io.Reader, clientW io.Writer, upstreamR io.Reader, upstreamW io.Writer) {
	done := make(chan struct{}, 2)
	go func() {
		_, _ = io.Copy(upstreamW, clientR)
		done <- struct{}{}
	}()
	go func() {
		_, _ = io.Copy(clientW, upstreamR)
		done <- struct{}{}
	}()
	<-done
}

// recordLength reads the 5-byte header of a TLS record and returns the
// length of the record body. It reports [ErrNotHandshake] when the record
// is not a handshake record, and [ErrTruncated] when header is too short.
func recordLength(header []byte) (int, error) {
	if len(header) < 5 {
		return 0, ErrTruncated
	}
	if header[0] != recordTypeHandshake {
		return 0, ErrNotHandshake
	}
	return int(header[3])<<8 | int(header[4]), nil
}

// parseClientHelloSNI extracts the server name from a TLS ClientHello. data
// holds one full TLS record: the 5-byte record header followed by the
// handshake message.
func parseClientHelloSNI(data []byte) (string, error) {
	n, err := recordLength(data)
	if err != nil {
		return "", err
	}
	if len(data) < 5+n {
		return "", ErrTruncated
	}
	hs := data[5 : 5+n]

	if len(hs) < 4 {
		return "", ErrTruncated
	}
	if hs[0] != handshakeTypeClientHello {
		return "", ErrNotHandshake
	}
	hsLen := int(hs[1])<<16 | int(hs[2])<<8 | int(hs[3])
	if len(hs) < 4+hsLen {
		return "", ErrTruncated
	}

	return serverNameFromClientHello(hs[4 : 4+hsLen])
}

// serverNameFromClientHello walks the body of a ClientHello message, past
// the version, the random, the session ID, the cipher suites, and the
// compression methods, to the extensions.
func serverNameFromClientHello(body []byte) (string, error) {
	r := reader{buf: body}

	if !r.skip(2 + 32) { // client_version, random
		return "", ErrTruncated
	}
	sessionIDLen, ok := r.byte()
	if !ok || !r.skip(int(sessionIDLen)) {
		return "", ErrTruncated
	}
	cipherSuitesLen, ok := r.uint16()
	if !ok || !r.skip(cipherSuitesLen) {
		return "", ErrTruncated
	}
	compressionLen, ok := r.byte()
	if !ok || !r.skip(int(compressionLen)) {
		return "", ErrTruncated
	}

	if r.remaining() == 0 {
		return "", ErrNoSNI
	}
	extensionsLen, ok := r.uint16()
	if !ok {
		return "", ErrTruncated
	}
	extensions, ok := r.take(extensionsLen)
	if !ok {
		return "", ErrTruncated
	}

	return serverNameFromExtensions(extensions)
}

// serverNameFromExtensions walks a TLS extensions list and returns the host
// name that the server_name extension carries.
func serverNameFromExtensions(data []byte) (string, error) {
	r := reader{buf: data}
	for r.remaining() > 0 {
		extType, ok := r.uint16()
		if !ok {
			return "", ErrTruncated
		}
		extLen, ok := r.uint16()
		if !ok {
			return "", ErrTruncated
		}
		extBody, ok := r.take(extLen)
		if !ok {
			return "", ErrTruncated
		}
		if extType != extensionServerName {
			continue
		}
		return serverNameFromExtension(extBody)
	}
	return "", ErrNoSNI
}

// serverNameFromExtension reads the body of a server_name extension and
// returns the first host_name entry it carries.
func serverNameFromExtension(data []byte) (string, error) {
	r := reader{buf: data}
	listLen, ok := r.uint16()
	if !ok {
		return "", ErrTruncated
	}
	list, ok := r.take(listLen)
	if !ok {
		return "", ErrTruncated
	}

	lr := reader{buf: list}
	for lr.remaining() > 0 {
		nameType, ok := lr.byte()
		if !ok {
			return "", ErrTruncated
		}
		nameLen, ok := lr.uint16()
		if !ok {
			return "", ErrTruncated
		}
		name, ok := lr.take(nameLen)
		if !ok {
			return "", ErrTruncated
		}
		if nameType == serverNameTypeHostName {
			return string(name), nil
		}
	}
	return "", ErrNoSNI
}

// reader reads a byte slice forward. Every method reports false instead of
// panicking when the slice runs out.
type reader struct {
	buf []byte
	pos int
}

func (r *reader) remaining() int { return len(r.buf) - r.pos }

func (r *reader) take(n int) ([]byte, bool) {
	if n < 0 || n > r.remaining() {
		return nil, false
	}
	b := r.buf[r.pos : r.pos+n]
	r.pos += n
	return b, true
}

// skip advances the reader by n bytes and reports whether that many bytes
// were left.
func (r *reader) skip(n int) bool {
	_, ok := r.take(n)
	return ok
}

func (r *reader) byte() (byte, bool) {
	b, ok := r.take(1)
	if !ok {
		return 0, false
	}
	return b[0], true
}

func (r *reader) uint16() (int, bool) {
	b, ok := r.take(2)
	if !ok {
		return 0, false
	}
	return int(b[0])<<8 | int(b[1]), true
}
