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
	"regexp"
	"strconv"
	"strings"
	"sync"
	"testing"

	"github.com/stretchr/testify/require"
	"github.com/stretchr/testify/suite"

	"github.com/justenwalker/kevin/internal/config"
	"github.com/justenwalker/kevin/internal/container"
	"github.com/justenwalker/kevin/internal/container/docker"
	"github.com/justenwalker/kevin/internal/pki/ca"
	"github.com/justenwalker/kevin/internal/relay"
	"github.com/justenwalker/kevin/internal/statedir"
	"github.com/justenwalker/kevin/internal/step"
)

// newTestAuthority builds a fresh project authority for a test - relay.Start
// mints the control channel's certificates off it.
func newTestAuthority(t *testing.T) *ca.CA {
	t.Helper()
	t.Setenv(statedir.UserStateDirEnv, t.TempDir())
	t.Setenv(statedir.ProjectStateDirEnv, t.TempDir())

	m := ca.NewManager("cwd", "", "demo", ca.Options{})
	authority, err := m.LoadOrGenerateIntermediate()
	require.NoError(t, err)
	return authority
}

// RelaySuite runs a full engine.Run against a real docker daemon.
//
// Tier: integration.
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

	bin, err := echoPlugin()
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

	var env step.Env
	err := Run(ctx, Options{
		Dir:    dir,
		Scope:  config.ScopeEnv,
		Events: w,
		OnEnvironment: func(e step.Env) {
			env = e
		},
	})
	s.Require().NoError(err)

	s.NotEmpty(env.Relay, "the environment must carry the relay address")

	names, err := dockerClient.ListByLabel(context.Background(), container.LabelProject, project)
	s.Require().NoError(err)
	s.Empty(names, "no container may carry the project label once the run ends")

	_, err = dockerClient.NetworkGateway(context.Background(), networkName(project))
	s.Require().ErrorIs(err, container.ErrNotFound, "the network must be gone once the run ends")
}

// TestReapLeavesALiveRelayInPlace extends TestReapSkipsTheRelay to the live
// case: a real relay container runs, and a real reap call must not remove
// it while it still serves the session.
func (s *RelaySuite) TestReapLeavesALiveRelayInPlace() {
	t := s.T()
	ctx := t.Context()
	const project = "kevin-it-super-reap"
	network := networkName(project)

	s.Require().NoError(dockerClient.NetworkCreate(ctx, network, container.NetworkOptions{
		Labels: map[string]string{container.LabelProject: project},
	}))
	t.Cleanup(func() { _ = dockerClient.NetworkRemove(context.WithoutCancel(context.Background()), network) })

	rl, err := relay.Start(ctx, dockerClient, relay.Options{
		Project:   project,
		Network:   network,
		Domain:    "kevin.home",
		ProxyAddr: "host.docker.internal:18080",
		Image:     relay.Ref(""),
		Authority: newTestAuthority(t),
	})
	s.Require().NoError(err)
	t.Cleanup(func() { _ = rl.Close() })

	orphanName := "kevin-" + project + "-orphan"
	_, err = dockerClient.Run(ctx, container.RunSpec{
		Image: "busybox:stable",
		Name:  orphanName,
		Cmd:   []string{"sleep", "300"},
		Labels: map[string]string{
			container.LabelProject: project,
		},
	})
	s.Require().NoError(err)
	t.Cleanup(func() { _ = dockerClient.Remove(context.WithoutCancel(context.Background()), orphanName) })

	// reap also removes the network of the project. The relay still joins it
	// here, unlike the normal flow where the caller closes the relay first,
	// so this call reports an error on that step. The container-level
	// assertions below are what this test proves.
	r := &run{cfg: &config.Config{Project: project}, runtime: dockerClient, events: io.Discard}
	_ = r.reap(ctx)

	relayName := "kevin-" + project + "-relay"
	_, err = dockerClient.Inspect(ctx, relayName)
	s.Require().NoError(err, "reap must leave a live relay container in place")

	_, err = dockerClient.Inspect(ctx, orphanName)
	s.Require().ErrorIs(err, container.ErrNotFound, "reap must still remove an orphan that carries no role")
}

// echoPlugin is the path of the compiled echo plugin, built once.
var echoPlugin = sync.OnceValues(buildEchoPlugin)

// dockerClient inspects docker resources directly; these tests run on docker.
var dockerClient = docker.Client{}

func buildEchoPlugin() (string, error) {
	dir, err := os.MkdirTemp("", "kevin-plugin-*")
	if err != nil {
		return "", err
	}
	bin := filepath.Join(dir, "kevin-plugin-echo")
	cmd := exec.CommandContext(context.Background(), "go", "build", "-o", bin,
		"github.com/justenwalker/kevin/cmd/kevin-plugin-echo")
	if out, buildErr := cmd.CombinedOutput(); buildErr != nil {
		return "", fmt.Errorf("build echo plugin: %w: %s", buildErr, out)
	}
	return bin, nil
}

// project writes a kevin.cue fixture, with the echo plugin and free ports,
// and returns its directory. Unless body sets its own project field, the
// project name is unique per call: reusing the directory's sequence-number
// name would recreate one Docker network back to back and hit Docker's own
// removal/recreate race.
func project(t *testing.T, body string) string {
	t.Helper()
	bin, err := echoPlugin()
	require.NoError(t, err)

	dir := t.TempDir()
	src := "plugins: echo: cmd: " + strconv.Quote(bin) + "\n" + proxyBlock(t)
	if !strings.Contains(body, "project:") {
		name := config.SlugName(t.Name()) + "-" + filepath.Base(dir)
		src += "project: " + strconv.Quote(name) + "\n"
		removeProject(t, name)
	} else if m := projectField.FindStringSubmatch(body); m != nil {
		removeProject(t, m[1])
	}
	src += body
	require.NoError(t, os.WriteFile(filepath.Join(dir, "kevin.cue"), []byte(src), 0o600))
	return dir
}

// projectField finds the name of a quoted project field in a fixture body.
var projectField = regexp.MustCompile(`(?m)^\s*project:\s*"([^"]+)"`)

// removeProject removes, when t ends, the containers and the network that a
// run with Keep leaves behind for the project called name. A failure is
// ignored: the run may have removed them already.
func removeProject(t *testing.T, name string) {
	t.Helper()
	t.Cleanup(func() {
		ctx := context.WithoutCancel(t.Context())
		containers, _ := dockerClient.ListByLabel(ctx, container.LabelProject, name)
		for _, container := range containers {
			_ = dockerClient.Remove(ctx, container)
		}
		_ = dockerClient.NetworkRemove(ctx, networkName(name))
	})
}
