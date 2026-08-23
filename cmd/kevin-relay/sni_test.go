package main

import (
	"bufio"
	"crypto/tls"
	"net"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// captureClientHello drives a real TLS handshake with cfg over an in-memory
// pipe, and returns the raw ClientHello record that the client sends.
func captureClientHello(t *testing.T, cfg *tls.Config) []byte {
	t.Helper()

	clientConn, serverConn := net.Pipe()
	t.Cleanup(func() {
		_ = clientConn.Close()
		_ = serverConn.Close()
	})

	go func() {
		// No server ever answers, so the handshake always fails once the
		// ClientHello is sent. The test only needs the bytes on the wire.
		_ = tls.Client(clientConn, cfg).HandshakeContext(t.Context())
	}()

	br := bufio.NewReaderSize(serverConn, maxClientHello)
	hello, err := readClientHello(br)
	require.NoError(t, err, "readClientHello must read the record that the real handshake produced")
	return hello
}

func TestParseClientHelloSNI(t *testing.T) {
	t.Run("from a real handshake", func(t *testing.T) {
		hello := captureClientHello(t, &tls.Config{ServerName: "web.kevin.home", InsecureSkipVerify: true})

		got, err := parseClientHelloSNI(hello)

		require.NoError(t, err)
		assert.Equal(t, "web.kevin.home", got)
	})

	t.Run("with no server name", func(t *testing.T) {
		// A literal IP as ServerName is never sent as SNI (RFC 6066), so the
		// real handshake produces a ClientHello with no server_name
		// extension.
		hello := captureClientHello(t, &tls.Config{ServerName: "127.0.0.1", InsecureSkipVerify: true})

		_, err := parseClientHelloSNI(hello)

		require.ErrorIs(t, err, ErrNoSNI)
	})

	t.Run("table cases", func(t *testing.T) {
		validHello := captureClientHello(t, &tls.Config{ServerName: "api.kevin.home", InsecureSkipVerify: true})

		tests := []struct {
			name    string
			data    []byte
			want    string
			wantErr error
		}{
			{name: "a real client hello", data: validHello, want: "api.kevin.home"},
			{name: "a truncated record", data: []byte{0x16, 0x03, 0x01, 0x00, 0x05, 0x01}, wantErr: ErrTruncated},
			{name: "too short to hold a record header", data: []byte{0x16, 0x03}, wantErr: ErrTruncated},
			{
				name:    "a record that is not a handshake",
				data:    []byte{0x17, 0x03, 0x03, 0x00, 0x01, 0xAA},
				wantErr: ErrNotHandshake,
			},
		}

		for _, tt := range tests {
			t.Run(tt.name, func(t *testing.T) {
				got, err := parseClientHelloSNI(tt.data)
				if tt.wantErr != nil {
					require.ErrorIs(t, err, tt.wantErr)
					return
				}
				require.NoError(t, err)
				assert.Equal(t, tt.want, got)
			})
		}
	})
}

func TestRecordLength(t *testing.T) {
	tests := []struct {
		name    string
		header  []byte
		want    int
		wantErr error
	}{
		{name: "a handshake record", header: []byte{0x16, 0x03, 0x03, 0x01, 0x2c}, want: 0x012c},
		{name: "too short", header: []byte{0x16, 0x03}, wantErr: ErrTruncated},
		{name: "application data, not a handshake", header: []byte{0x17, 0x03, 0x03, 0x00, 0x10}, wantErr: ErrNotHandshake},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := recordLength(tt.header)
			if tt.wantErr != nil {
				require.ErrorIs(t, err, tt.wantErr)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tt.want, got)
		})
	}
}

func TestIsConnectOK(t *testing.T) {
	tests := []struct {
		name string
		line string
		want bool
	}{
		{name: "a 200 with the default reason", line: "HTTP/1.1 200 Connection Established\r\n", want: true},
		{name: "a 200 with no reason", line: "HTTP/1.1 200\r\n", want: true},
		{name: "a 403", line: "HTTP/1.1 403 Forbidden\r\n", want: false},
		{name: "a 502", line: "HTTP/1.1 502 Bad Gateway\r\n", want: false},
		{name: "a short line", line: "junk", want: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, isConnectOK(tt.line))
		})
	}
}

func TestHandleHTTPSPipesTheClientHelloAndTheDataAfterIt(t *testing.T) {
	// A fake proxy that accepts one CONNECT, answers 200, then echoes
	// whatever it receives back to the caller.
	var lc net.ListenConfig
	ln, err := lc.Listen(t.Context(), "tcp", "127.0.0.1:0")
	require.NoError(t, err)
	t.Cleanup(func() { _ = ln.Close() })

	go func() {
		conn, err := ln.Accept()
		if err != nil {
			return
		}
		defer func() { _ = conn.Close() }()

		br := bufio.NewReader(conn)
		if _, err := br.ReadString('\n'); err != nil { // request line
			return
		}
		for { // headers, up to the blank line
			line, err := br.ReadString('\n')
			if err != nil || line == "\r\n" {
				break
			}
		}
		if _, err := conn.Write([]byte("HTTP/1.1 200 Connection Established\r\n\r\n")); err != nil {
			return
		}

		// One read and one write back is enough to prove the ClientHello
		// bytes reach the upstream. The write's close then ends the pipe on
		// both sides, so the test does not hang.
		buf := make([]byte, 4096)
		n, _ := br.Read(buf)
		if n > 0 {
			_, _ = conn.Write(buf[:n])
		}
	}()

	clientConn, serverConn := net.Pipe()
	t.Cleanup(func() {
		_ = clientConn.Close()
		_ = serverConn.Close()
	})

	go func() {
		cfg := &tls.Config{ServerName: "echo.kevin.home", InsecureSkipVerify: true}
		_ = tls.Client(clientConn, cfg).HandshakeContext(t.Context())
	}()

	done := make(chan struct{})
	go func() {
		handleHTTPS(t.Context(), serverConn, ln.Addr().String())
		close(done)
	}()

	<-done
}
