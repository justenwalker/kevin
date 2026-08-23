package main

import (
	"bufio"
	"bytes"
	"net"
	"net/http"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// startFakeProxy runs a minimal HTTP server that records the request line
// it received and answers with a fixed body. It returns the listen address.
func startFakeProxy(t *testing.T, gotLine chan<- string) string {
	t.Helper()

	var lc net.ListenConfig
	ln, err := lc.Listen(t.Context(), "tcp", "127.0.0.1:0")
	require.NoError(t, err)
	t.Cleanup(func() { _ = ln.Close() })

	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			go func() {
				defer func() { _ = conn.Close() }()
				br := bufio.NewReader(conn)
				line, err := br.ReadString('\n')
				if err != nil {
					return
				}
				gotLine <- line
				for { // drain headers up to the blank line
					h, err := br.ReadString('\n')
					if err != nil || h == "\r\n" {
						break
					}
				}
				_, _ = conn.Write([]byte("HTTP/1.1 200 OK\r\nContent-Length: 2\r\n\r\nok"))
			}()
		}
	}()

	return ln.Addr().String()
}

func TestForwardHTTPUsesAbsoluteURIProxyForm(t *testing.T) {
	lines := make(chan string, 1)
	proxyAddr := startFakeProxy(t, lines)

	req, err := http.ReadRequest(bufio.NewReader(strings.NewReader(
		"GET /path?x=1 HTTP/1.1\r\nHost: web.kevin.home\r\n\r\n")))
	require.NoError(t, err)

	var out bytes.Buffer
	err = forwardHTTP(t.Context(), &out, proxyAddr, req)
	require.NoError(t, err)

	line := <-lines
	assert.Equal(t, "GET http://web.kevin.home/path?x=1 HTTP/1.1\r\n", line,
		"the request line sent to the proxy must carry the absolute URI form")
	assert.Contains(t, out.String(), "200 OK")
}

func TestHandleHTTPLoopsOverKeepAlive(t *testing.T) {
	lines := make(chan string, 2)
	proxyAddr := startFakeProxy(t, lines)

	client, server := net.Pipe()
	t.Cleanup(func() {
		_ = client.Close()
		_ = server.Close()
	})

	done := make(chan struct{})
	go func() {
		handleHTTP(t.Context(), server, proxyAddr)
		close(done)
	}()

	_, err := client.Write([]byte("GET / HTTP/1.1\r\nHost: web.kevin.home\r\n\r\n"))
	require.NoError(t, err)
	br := bufio.NewReader(client)
	resp, err := http.ReadResponse(br, nil)
	require.NoError(t, err)
	assert.Equal(t, 200, resp.StatusCode)
	_ = resp.Body.Close()
	assert.Equal(t, "GET http://web.kevin.home/ HTTP/1.1\r\n", <-lines)

	_, err = client.Write([]byte("GET /second HTTP/1.1\r\nHost: api.kevin.home\r\n\r\n"))
	require.NoError(t, err)
	resp2, err := http.ReadResponse(br, nil)
	require.NoError(t, err)
	assert.Equal(t, 200, resp2.StatusCode)
	_ = resp2.Body.Close()
	assert.Equal(t, "GET http://api.kevin.home/second HTTP/1.1\r\n", <-lines,
		"a second request on the same connection must also be forwarded")

	_ = client.Close()
	<-done
}
