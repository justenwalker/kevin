package proxy

import (
	"bufio"
	"io"
	"net"
	"net/http"
	"strings"
)

// isUpgrade reports whether r asks to switch protocols.
func isUpgrade(r *http.Request) bool {
	return r.Header.Get("Upgrade") != "" && hasToken(r.Header.Get("Connection"), "upgrade")
}

// hasToken reports whether token appears, case-insensitively, among the
// comma-separated values of header.
func hasToken(header, token string) bool {
	for part := range strings.SplitSeq(header, ",") {
		if strings.EqualFold(strings.TrimSpace(part), token) {
			return true
		}
	}
	return false
}

// serveUpgrade handles the upgrade request.
// It dials the request target directly, forwards the handshake, and upon  successful switch,
// pipes bytes bidirectionally for the rest of the connection.
//
// The proxy does not parse the frames directly, since it is an opaque stream.
func (p *Proxy) serveUpgrade(w http.ResponseWriter, r *http.Request) int {
	hj, ok := w.(http.Hijacker)
	if !ok {
		http.Error(w, "upgrade not supported", http.StatusInternalServerError)
		return http.StatusInternalServerError
	}
	client, crw, err := hj.Hijack()
	if err != nil {
		log.Ctx(r.Context()).Debug("upgrade hijack failed", "error", err)
		return 0
	}
	defer client.Close() //nolint:errcheck // best effort once the pipe (or an earlier error) ends

	upstream, err := p.dialContext(r.Context(), "tcp", r.URL.Host)
	if err != nil {
		log.Ctx(r.Context()).Debug("upgrade dial failed", "host", r.URL.Host, "error", err)
		_, _ = io.WriteString(client, "HTTP/1.1 502 Bad Gateway\r\n\r\n")
		return http.StatusBadGateway
	}
	defer upstream.Close() //nolint:errcheck // best effort once the pipe (or an earlier error) ends

	if writeErr := r.Write(upstream); writeErr != nil {
		log.Ctx(r.Context()).Debug("upgrade request write failed", "error", writeErr)
		return 0
	}

	// buffer the upstream so that ReadResponse can read the response headers.
	// We do not want to read part of the response body, since we are piping it directly.
	br := bufio.NewReader(upstream)
	resp, err := http.ReadResponse(br, r)
	if err != nil {
		log.Ctx(r.Context()).Debug("upgrade response read failed", "error", err)
		return 0
	}
	defer resp.Body.Close() //nolint:errcheck // a 101 response carries no body; best effort regardless

	if writeErr := resp.Write(client); writeErr != nil {
		return resp.StatusCode
	}
	if resp.StatusCode == http.StatusSwitchingProtocols {
		pipeUpgrade(client, upstream, br, crw.Reader)
	}
	return resp.StatusCode
}

// pipeUpgrade pipes data bidirectionally between client and upstream.
// upstreamBuffered and clientBuffered are the bufio.Readers Hijack/ReadResponse
// already drained bytes into; reading through them (rather than the raw
// conns) preserves any bytes buffered ahead of the handshake boundary on
// either side.
func pipeUpgrade(client, upstream net.Conn, upstreamBuffered, clientBuffered *bufio.Reader) {
	done := make(chan struct{}, 2)
	go func() { _, _ = io.Copy(client, upstreamBuffered); done <- struct{}{} }()
	go func() { _, _ = io.Copy(upstream, clientBuffered); done <- struct{}{} }()
	<-done
}
