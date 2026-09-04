//go:build integration

package engine

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"

	"github.com/stretchr/testify/suite"

	"github.com/justenwalker/kevin/internal/config"
	"github.com/justenwalker/kevin/internal/cri"
	"github.com/justenwalker/kevin/internal/relay"
	"github.com/justenwalker/kevin/protos/pb"
)

// RelaySuite runs a full engine.Run against a real docker daemon.
type RelaySuite struct {
	suite.Suite

	echoPlugin string
}

func TestRelaySuite(t *testing.T) {
	suite.Run(t, new(RelaySuite))
}

// SetupSuite builds the echo plugin and makes sure the relay image exists.
func (s *RelaySuite) SetupSuite() {
	t := s.T()
	requireRelay(t)

	bin, err := buildEchoPluginForIntegration(t)
	s.Require().NoError(err)
	s.echoPlugin = bin
}

// integrationWatcher collects the events that Run writes, and cancels the
// run once a chosen line arrives.
type integrationWatcher struct {
	mu     sync.Mutex
	buf    bytes.Buffer
	until  string
	cancel context.CancelFunc
}

func (w *integrationWatcher) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	n, err := w.buf.Write(p)
	if w.until != "" && strings.Contains(w.buf.String(), w.until) {
		w.until = ""
		w.cancel()
	}
	return n, err
}

// writeRelayProject writes a kevin.cue that declares the echo plugin and one
// step, and returns the project directory.
func (s *RelaySuite) writeRelayProject(project string) string {
	t := s.T()
	dir := t.TempDir()
	src := proxyBlock(t) + `plugins: echo: cmd: ` + strconv.Quote(s.echoPlugin) + `
project: ` + strconv.Quote(project) + `
env: web: {uses: "echo:echo", with: message: "hi"}
`
	s.Require().NoError(os.WriteFile(filepath.Join(dir, "kevin.cue"), []byte(src), 0o600))
	return dir
}

// TestRunStartsRelayAndCleansUpEveryResource proves that a run populates the
// environment with a relay address, and that nothing of the project
// survives after the run ends.
func (s *RelaySuite) TestRunStartsRelayAndCleansUpEveryResource() {
	t := s.T()
	const project = "kevin-it-super"
	dir := s.writeRelayProject(project)

	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	w := &integrationWatcher{until: "web              ready", cancel: cancel}

	var env *pb.Environment
	err := Run(ctx, Options{
		Dir:    dir,
		Scope:  config.ScopeEnv,
		Events: w,
		OnEnvironment: func(e *pb.Environment) {
			env = e
		},
	})
	s.Require().NoError(err)

	s.Require().NotNil(env, "OnEnvironment must run before any step")
	s.NotEmpty(env.GetRelay(), "the environment must carry the relay address")

	names, err := dockerClient.ListByLabel(context.Background(), cri.LabelProject, project)
	s.Require().NoError(err)
	s.Empty(names, "no container may carry the project label once the run ends")

	_, err = dockerClient.NetworkGateway(context.Background(), NetworkName(project))
	s.Require().ErrorIs(err, cri.ErrNotFound, "the network must be gone once the run ends")
}

// TestReapLeavesALiveRelayInPlace extends TestReapSkipsTheRelay to the live
// case: a real relay container runs, and a real reap call must not remove
// it while it still serves the session.
func (s *RelaySuite) TestReapLeavesALiveRelayInPlace() {
	t := s.T()
	ctx := t.Context()
	const project = "kevin-it-super-reap"
	network := NetworkName(project)

	s.Require().NoError(dockerClient.NetworkCreate(ctx, network, map[string]string{
		cri.LabelProject: project,
	}))
	t.Cleanup(func() { _ = dockerClient.NetworkRemove(context.WithoutCancel(context.Background()), network) })

	rl, err := relay.Start(ctx, relay.Options{
		Project:   project,
		Network:   network,
		Domain:    "kevin.home",
		ProxyAddr: "host.docker.internal:18080",
		Image:     relay.Ref(""),
	})
	s.Require().NoError(err)
	t.Cleanup(func() { _ = rl.Close() })

	orphanName := "kevin-" + project + "-orphan"
	_, err = dockerClient.Run(ctx, cri.RunSpec{
		Image: "busybox:stable",
		Name:  orphanName,
		Cmd:   []string{"sleep", "300"},
		Labels: map[string]string{
			cri.LabelProject: project,
		},
	})
	s.Require().NoError(err)
	t.Cleanup(func() { _ = dockerClient.Remove(context.WithoutCancel(context.Background()), orphanName) })

	// reap also removes the network of the project. The relay still joins it
	// here, unlike the normal flow where the caller closes the relay first,
	// so this call reports an error on that step. The container-level
	// assertions below are what this test proves.
	r := &run{cfg: &config.Config{Project: project}, events: io.Discard}
	_ = r.reap(ctx)

	relayName := "kevin-" + project + "-relay"
	_, err = dockerClient.Inspect(ctx, relayName)
	s.Require().NoError(err, "reap must leave a live relay container in place")

	_, err = dockerClient.Inspect(ctx, orphanName)
	s.Require().ErrorIs(err, cri.ErrNotFound, "reap must still remove an orphan that carries no role")
}

// buildEchoPluginForIntegration builds the echo plugin binary, the same way
// buildEchoPlugin in engine_test.go does.
func buildEchoPluginForIntegration(t *testing.T) (string, error) {
	t.Helper()

	dir := t.TempDir()
	bin := filepath.Join(dir, "kevin-plugin-echo")
	cmd := exec.CommandContext(t.Context(), "go", "build", "-o", bin,
		"github.com/justenwalker/kevin/cmd/kevin-plugin-echo")
	if out, err := cmd.CombinedOutput(); err != nil {
		return "", fmt.Errorf("build echo plugin: %w: %s", err, out)
	}
	return bin, nil
}
