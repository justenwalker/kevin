//go:build e2e

package e2e

import (
	"fmt"
	"strconv"
	"syscall"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/stretchr/testify/suite"
)

// MCPSuite covers docs/MANUAL_TESTING.md section 18: a plugin-declared MCP
// tool (CallTool) reaching a real MCP client, alongside the five builtin
// tools. Imports the mcp go-sdk directly - a third-party protocol client,
// not a kevin package, the same one internal/engine/engine_test.go already
// uses for the same purpose - so this stays within e2e_test.go's "imports
// no kevin package" rule.
type MCPSuite struct {
	e2eSuite
}

func TestMCPSuite(t *testing.T) {
	suite.Run(t, new(MCPSuite))
}

// mcpCUE brings up two echo steps, "a" (needed by "b") and "b" - echo's
// step type ships a demo "echo" tool via Tools()/CallTool, namespaced
// "echo_echo_echo" once the engine collects it.
const mcpCUE = `project: "%s"

plugins: echo: cmd: %s

env: {
	a: {uses: "echo:echo", with: outputs: greeting: "hi"}
	b: {uses: "echo:echo", needs: ["a"], with: message: "hello"}
}
`

func (s *MCPSuite) TestPluginToolAppearsAndRoundTrips() {
	dir := s.T().TempDir()
	src := fmt.Sprintf(mcpCUE, "kevin-e2e-mcp-tool", strconv.Quote(s.echoPluginBin()))
	s.writeCUE(dir, src)

	p := s.startKevin(dir, "-C", dir, "run")
	s.waitFor(p, stepLine("b", "ready"), defaultTimeout)
	out := p.buf.String()
	s.T().Cleanup(func() {
		require := s.Require()
		require.NoError(p.cmd.Process.Signal(syscall.SIGINT))
		s.waitExit(p, defaultTimeout)
	})

	consoleAddr := s.consoleAddr(out)

	sess := s.connectMCP(consoleAddr)
	defer func() { _ = sess.Close() }()

	tools, err := sess.ListTools(s.T().Context(), nil)
	s.Require().NoError(err)
	var found bool
	for _, tl := range tools.Tools {
		if tl.Name == "echo_echo_echo" {
			found = true
		}
	}
	s.True(found, "echo's step type must advertise its tool, namespaced echo_echo_<tool>, alongside the five builtin tools")

	result, err := sess.CallTool(s.T().Context(), &mcp.CallToolParams{
		Name:      "echo_echo_echo",
		Arguments: map[string]any{"step": "b"},
	})
	s.Require().NoError(err)
	s.False(result.IsError, "%v", result.Content)

	got, ok := result.StructuredContent.(map[string]any)
	s.Require().True(ok, "expected structured content, got %#v", result.StructuredContent)
	s.Equal("hello", got["message"])
	deps, ok := got["deps"].(map[string]any)
	s.Require().True(ok, "expected a deps object, got %#v", got["deps"])
	s.Contains(deps, "a", "the tool call must resolve b's own deps, the same as a real Up would")
}

func (s *MCPSuite) TestPluginToolRejectsAWrongStepName() {
	dir := s.T().TempDir()
	src := fmt.Sprintf(mcpCUE, "kevin-e2e-mcp-wrongstep", strconv.Quote(s.echoPluginBin()))
	s.writeCUE(dir, src)

	p := s.startKevin(dir, "-C", dir, "run")
	s.waitFor(p, stepLine("b", "ready"), defaultTimeout)
	out := p.buf.String()
	s.T().Cleanup(func() {
		require := s.Require()
		require.NoError(p.cmd.Process.Signal(syscall.SIGINT))
		s.waitExit(p, defaultTimeout)
	})

	consoleAddr := s.consoleAddr(out)

	sess := s.connectMCP(consoleAddr)
	defer func() { _ = sess.Close() }()

	_, err := sess.CallTool(s.T().Context(), &mcp.CallToolParams{
		Name:      "echo_echo_echo",
		Arguments: map[string]any{"step": "no-such-step"},
	})
	s.Require().Error(err, "naming a step that doesn't exist must be a clear MCP-level error, not a silent empty result")
}

// consoleAddr extracts the console's address from kevin's own startup
// banner in out, failing the test if it's missing.
func (s *MCPSuite) consoleAddr(out string) string {
	for _, row := range addrRE.FindAllStringSubmatch(out, -1) {
		if row[1] == "console" {
			return row[2]
		}
	}
	s.Require().Fail("no console address in output", "output:\n%s", out)
	return ""
}

// connectMCP connects an mcp.Client to addr's Streamable HTTP endpoint,
// mounted at /_mcp on the console's own listener.
func (s *MCPSuite) connectMCP(consoleAddr string) *mcp.ClientSession {
	client := mcp.NewClient(&mcp.Implementation{Name: "e2e", Version: "0"}, nil)
	sess, err := client.Connect(s.T().Context(), &mcp.StreamableClientTransport{
		Endpoint: "http://" + consoleAddr + "/_mcp",
	}, nil)
	s.Require().NoError(err)
	return sess
}
