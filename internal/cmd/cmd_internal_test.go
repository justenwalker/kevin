package cmd

import (
	"bytes"
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/justenwalker/kevin/protos/pb"
)

func TestPrintEnvironmentInfo(t *testing.T) {
	var buf bytes.Buffer
	printEnvironmentInfo(&buf, &pb.Environment{
		ConsoleAddr:   "127.0.0.1:18081",
		HttpProxyAddr: "127.0.0.1:18080",
	})

	out := buf.String()
	assert.Contains(t, out, "http://127.0.0.1:18081")
	assert.Contains(t, out, "http://127.0.0.1:18080")
	assert.Contains(t, out, "http://127.0.0.1:18081/_mcp")
	assert.Contains(t, out, "export HTTP_PROXY=http://127.0.0.1:18080 HTTPS_PROXY=http://127.0.0.1:18080")
	assert.Contains(t, out, "claude mcp add --transport http kevin http://127.0.0.1:18081/_mcp")
}
