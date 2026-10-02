package kubernetes

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/justenwalker/kevin/internal/clusterrelay"
	"github.com/justenwalker/kevin/internal/command"
	kindcmd "github.com/justenwalker/kevin/internal/command/kind"
	"github.com/justenwalker/kevin/internal/cri"
	"github.com/justenwalker/kevin/internal/docker"
	"github.com/justenwalker/kevin/plugin"
)

// dockerClient inspects docker resources directly in tests - every test
// here runs against the default (docker) engine.
var dockerClient = docker.Client{}

// capture records what a step logs, as a fake plugin.Emitter.
type capture struct {
	stdout []string
	stderr []string
}

func (c *capture) Log(stream, text string) {
	if stream == "stderr" {
		c.stderr = append(c.stderr, text)
		return
	}
	c.stdout = append(c.stdout, text)
}

func (c *capture) Progress(string, int64, int64) {}

// fakeRuntime is a hand-written cri.Runtime double. It lets a test drive the
// kubectl-over-Exec orchestration in capture.go, coredns.go, and trustca.go
// without a live docker daemon or kind cluster.
type fakeRuntime struct {
	exec      func(ctx context.Context, container string, args ...string) (string, error)
	execInput func(ctx context.Context, container string, stdin io.Reader, args ...string) (string, error)
	inspect   func(ctx context.Context, container string) (cri.Container, error)
	run       func(ctx context.Context, spec cri.RunSpec) (string, error)
	save      func(ctx context.Context, image string) (io.ReadCloser, error)

	listByLabel func(ctx context.Context, key, value string) ([]string, error)

	networkConnect func(ctx context.Context, network, container string) error
	networkCreate  func(ctx context.Context, name string, opts cri.NetworkOptions) error
	networkRemove  func(ctx context.Context, name string) error
}

var _ cri.Runtime = fakeRuntime{}

func (f fakeRuntime) Exec(ctx context.Context, container string, args ...string) (string, error) {
	if f.exec == nil {
		return "", nil
	}
	return f.exec(ctx, container, args...)
}

func (f fakeRuntime) ExecInput(ctx context.Context, container string, stdin io.Reader, args ...string) (string, error) {
	if f.execInput == nil {
		return "", nil
	}
	return f.execInput(ctx, container, stdin, args...)
}

func (f fakeRuntime) Inspect(ctx context.Context, name string) (cri.Container, error) {
	if f.inspect == nil {
		return cri.Container{}, nil
	}
	return f.inspect(ctx, name)
}

func (fakeRuntime) Available(context.Context) error { return nil }

func (f fakeRuntime) Run(ctx context.Context, spec cri.RunSpec) (string, error) {
	if f.run == nil {
		return "", nil
	}
	return f.run(ctx, spec)
}

func (fakeRuntime) Remove(context.Context, string) error { return nil }

func (f fakeRuntime) Save(ctx context.Context, image string) (io.ReadCloser, error) {
	if f.save == nil {
		return io.NopCloser(strings.NewReader("")), nil
	}
	return f.save(ctx, image)
}

func (f fakeRuntime) NetworkCreate(ctx context.Context, name string, opts cri.NetworkOptions) error {
	if f.networkCreate == nil {
		return nil
	}
	return f.networkCreate(ctx, name, opts)
}

func (f fakeRuntime) NetworkRemove(ctx context.Context, name string) error {
	if f.networkRemove == nil {
		return nil
	}
	return f.networkRemove(ctx, name)
}

func (f fakeRuntime) NetworkConnect(ctx context.Context, network, container string) error {
	if f.networkConnect == nil {
		return nil
	}
	return f.networkConnect(ctx, network, container)
}

func (fakeRuntime) NetworkGateway(context.Context, string) (cri.Gateway, error) {
	return cri.Gateway{}, nil
}

func (f fakeRuntime) ListByLabel(ctx context.Context, key, value string) ([]string, error) {
	if f.listByLabel == nil {
		return nil, nil
	}
	return f.listByLabel(ctx, key, value)
}

// fakeDriver is a hand-written driver double. Each nil func field does
// nothing and succeeds.
type fakeDriver struct {
	kubectl      func(ctx context.Context, args ...string) (string, error)
	kubectlInput func(ctx context.Context, stdin io.Reader, args ...string) (string, error)
	pointDNS     func(ctx context.Context, nodes []string, relay string) error
	trustCA      func(ctx context.Context, nodes []string, caPEM string) error
	loadImage    func(ctx context.Context, path string) error
	labelNodes   func(ctx context.Context) error
	refresh      func(ctx context.Context) error
	nodes        func(ctx context.Context) ([]string, error)
	fingerprint  func(spec createSpec) (string, error)
	create       func(ctx context.Context, spec createSpec) ([]string, error)
}

var _ driver = fakeDriver{}

func (f fakeDriver) Nodes(ctx context.Context) ([]string, error) {
	if f.nodes == nil {
		return nil, nil
	}
	return f.nodes(ctx)
}

func (f fakeDriver) Fingerprint(spec createSpec) (string, error) {
	if f.fingerprint == nil {
		return "fake", nil
	}
	return f.fingerprint(spec)
}

func (f fakeDriver) Create(ctx context.Context, spec createSpec, _ plugin.Emitter) ([]string, error) {
	if f.create == nil {
		return nil, nil
	}
	return f.create(ctx, spec)
}

func (fakeDriver) Delete(context.Context, plugin.Emitter) error { return nil }

func (fakeDriver) Context() string { return "fake-context" }

func (fakeDriver) ControlPlane() string { return "demo-cluster-control-plane" }

func (f fakeDriver) Kubectl(ctx context.Context, args ...string) (string, error) {
	if f.kubectl == nil {
		return "", nil
	}
	return f.kubectl(ctx, args...)
}

func (f fakeDriver) KubectlInput(ctx context.Context, stdin io.Reader, args ...string) (string, error) {
	if f.kubectlInput == nil {
		return "", nil
	}
	return f.kubectlInput(ctx, stdin, args...)
}

func (f fakeDriver) RefreshAccess(ctx context.Context) error {
	if f.refresh == nil {
		return nil
	}
	return f.refresh(ctx)
}

func (f fakeDriver) LabelNodes(ctx context.Context) error {
	if f.labelNodes == nil {
		return nil
	}
	return f.labelNodes(ctx)
}

func (f fakeDriver) TrustCA(ctx context.Context, nodes []string, caPEM string, _ plugin.Emitter) error {
	if f.trustCA == nil {
		return nil
	}
	return f.trustCA(ctx, nodes, caPEM)
}

func (f fakeDriver) PointDNSAtRelay(ctx context.Context, nodes []string, relay string) error {
	if f.pointDNS == nil {
		return nil
	}
	return f.pointDNS(ctx, nodes, relay)
}

func (f fakeDriver) LoadImage(ctx context.Context, path string, _ plugin.Emitter) error {
	if f.loadImage == nil {
		return nil
	}
	return f.loadImage(ctx, path)
}

func TestSchemaCarriesTheEmbeddedSchema(t *testing.T) {
	schema := Step{}.Schema()

	assert.Contains(t, string(schema), "#Config")
	assert.Contains(t, string(schema), "workers")
}

func TestStep(t *testing.T) {
	assert.Equal(t, Step{}, New())
	assert.Equal(t, plugin.StepKindResource, Step{}.Kind())
	assert.True(t, Step{}.Idempotent(), "Up always removes a stale cluster before creating a fresh one")
}

func TestUpReportsABadWait(t *testing.T) {
	_, err := Step{}.Up(t.Context(), &plugin.UpRequest{
		Step:   "cluster",
		Config: []byte(`{"wait":"soon"}`),
	}, &capture{})

	require.Error(t, err)
	assert.Contains(t, err.Error(), "wait")
}

func TestUpReportsABadEngine(t *testing.T) {
	_, err := Step{}.Up(t.Context(), &plugin.UpRequest{
		Step:   "cluster",
		Config: []byte(`{}`),
		Env:    plugin.Env{Project: "demo", Workspace: t.TempDir(), Engine: "bogus"},
	}, &capture{})

	require.Error(t, err)
	assert.Contains(t, err.Error(), "bogus")
}

func TestFinishClusterSetup(t *testing.T) {
	nodes := []string{"demo-cluster-control-plane"}

	happyDriver := fakeDriver{kubectl: func(_ context.Context, args ...string) (string, error) {
		switch {
		case slices.Contains(args, "kubeadm-config"):
			return clusterConfigurationFixture, nil
		case slices.Contains(args, "get") && slices.Contains(args, "coredns"):
			return ".:53 {\n    forward . 8.8.8.8\n}\n", nil
		}
		return "", nil
	}}
	happyRT := fakeRuntime{inspect: func(_ context.Context, name string) (cri.Container, error) {
		return cri.Container{ID: name + "-id", NetnsPath: "/proc/1/ns/net"}, nil
	}}

	t.Run("with every opt-out set, does nothing", func(t *testing.T) {
		got, err := finishClusterSetup(t.Context(), fakeRuntime{}, fakeDriver{}, config{},
			plugin.Env{}, nodes, clusterrelay.ForwarderSpec{}, &capture{})
		require.NoError(t, err)
		assert.Nil(t, got.Exposed)
		assert.Nil(t, got.Containers)
	})

	t.Run("propagates a trust CA failure", func(t *testing.T) {
		env := plugin.Env{CAPath: filepath.Join(t.TempDir(), "missing.pem")}
		_, err := finishClusterSetup(t.Context(), happyRT, happyDriver, config{TrustCA: true}, env, nodes, clusterrelay.ForwarderSpec{}, &capture{})
		require.Error(t, err)
		assert.Contains(t, err.Error(), "read the kevin root certificate")
	})

	t.Run("propagates a coredns patch failure", func(t *testing.T) {
		env := plugin.Env{Domain: "kevin.home", Relay: "10.244.0.5:53"}
		drv := fakeDriver{kubectl: func(context.Context, ...string) (string, error) {
			return "", errors.New("exec failed")
		}}
		_, err := finishClusterSetup(t.Context(), fakeRuntime{}, drv, config{CoreDNS: true}, env, nodes, clusterrelay.ForwarderSpec{}, &capture{})
		require.Error(t, err)
		assert.Contains(t, err.Error(), "read the coredns Corefile")
	})

	t.Run("propagates a capture inspect failure", func(t *testing.T) {
		env := plugin.Env{Relay: "10.244.0.5:53"}
		rt := fakeRuntime{inspect: func(context.Context, string) (cri.Container, error) {
			return cri.Container{}, errors.New("no such container")
		}}
		_, err := finishClusterSetup(t.Context(), rt, happyDriver, config{}, env, nodes, clusterrelay.ForwarderSpec{}, &capture{})
		require.Error(t, err)
		assert.Contains(t, err.Error(), "inspect")
	})

	t.Run("propagates a relay failure", func(t *testing.T) {
		drv := fakeDriver{loadImage: func(context.Context, string) error { return errors.New("load failed") }}
		_, err := finishClusterSetup(t.Context(), fakeRuntime{}, drv, config{Relay: true}, plugin.Env{}, nodes, clusterrelay.ForwarderSpec{}, &capture{})
		require.Error(t, err)
		assert.Contains(t, err.Error(), "load the relay image")
	})

	t.Run("assembles containers when capture is on and no relay is wanted", func(t *testing.T) {
		env := plugin.Env{Relay: "10.244.0.5:53"}
		got, err := finishClusterSetup(t.Context(), happyRT, happyDriver, config{}, env, nodes, clusterrelay.ForwarderSpec{}, &capture{})
		require.NoError(t, err)
		assert.Nil(t, got.Exposed)
		require.Len(t, got.Containers, 1)
	})
}

func TestClusterOutputs(t *testing.T) {
	got := clusterOutputs(fakeDriver{}, "demo-cluster", "/workspace/kubeconfig/demo-cluster", []string{"demo-cluster-control-plane", "demo-cluster-worker"})

	assert.Equal(t, map[string]string{
		"name":       "demo-cluster",
		"kubeconfig": "/workspace/kubeconfig/demo-cluster",
		"context":    "fake-context",
		"nodes":      "demo-cluster-control-plane,demo-cluster-worker",
	}, got)
}

func TestExport(t *testing.T) {
	t.Run("reports broken JSON", func(t *testing.T) {
		_, err := Step{}.Export(t.Context(), &plugin.ExportRequest{Config: []byte(`{`)})
		require.Error(t, err)
	})

	t.Run("no kubeconfig yet is an error", func(t *testing.T) {
		_, err := Step{}.Export(t.Context(), &plugin.ExportRequest{
			Config: []byte(`{"driver":"kind"}`),
			Env:    plugin.Env{Project: "demo", Workspace: t.TempDir()},
		})
		require.Error(t, err)
		assert.Contains(t, err.Error(), "run `kevin run` or `kevin setup` first")
	})

	t.Run("reports the persisted values with no live containers when the engine can't be reached", func(t *testing.T) {
		workspace := t.TempDir()
		name := "demo-cluster"
		kubeconfig := filepath.Join(workspace, "kubeconfig", name)
		require.NoError(t, os.MkdirAll(filepath.Dir(kubeconfig), 0o700))
		require.NoError(t, os.WriteFile(kubeconfig, []byte("apiVersion: v1\n"), 0o600))

		got, err := Step{}.Export(t.Context(), &plugin.ExportRequest{
			Step:   "cluster",
			Config: []byte(`{"driver":"kind"}`),
			Env:    plugin.Env{Project: "demo", Workspace: workspace, Engine: "bogus"},
		})
		require.NoError(t, err)
		assert.Equal(t, plugin.StringMap(map[string]string{
			"name":       name,
			"kubeconfig": kubeconfig,
			"context":    "kind-" + name,
		}), got.Out)
		assert.Nil(t, got.Containers)
	})
}

func TestDownReportsBadConfig(t *testing.T) {
	err := Step{}.Down(t.Context(), &plugin.DownRequest{Config: []byte(`{`)}, &capture{})
	require.Error(t, err)
}

func TestDownIsIdempotent(t *testing.T) {
	requireDocker(t)
	requireKind(t)

	// A cluster that never existed is not an error. The supervisor calls Down
	// for every step of the setup scope, present or not.
	out := &capture{}
	err := Step{}.Down(t.Context(), &plugin.DownRequest{
		Step:   "cluster",
		Config: []byte(`{"driver":"kind"}`),
		Env: plugin.Env{
			Project:   "kevin-kind-absent",
			Workspace: t.TempDir(),
		},
	}, out)

	require.NoError(t, err)
	assert.Contains(t, strings.Join(out.stdout, "\n"), "removing cluster kevin-kind-absent-cluster")
}

func TestExportContainers(t *testing.T) {
	t.Run("no engine fails open", func(t *testing.T) {
		assert.Nil(t, exportContainers(t.Context(), nil, fakeDriver{}))
	})

	t.Run("a cluster that no longer exists fails open", func(t *testing.T) {
		assert.Nil(t, exportContainers(t.Context(), fakeRuntime{}, fakeDriver{}))
	})
}

func TestNewDriver(t *testing.T) {
	t.Run("kind", func(t *testing.T) {
		drv, err := newDriver(config{Driver: "kind"}, plugin.Env{}, "demo-cluster", "/kubeconfig", nil)
		require.NoError(t, err)
		assert.IsType(t, &kindDriver{}, drv)
	})

	t.Run("k3d", func(t *testing.T) {
		drv, err := newDriver(config{Driver: "k3d"}, plugin.Env{}, "demo-cluster", "/kubeconfig", nil)
		require.NoError(t, err)
		assert.IsType(t, &k3dDriver{}, drv)
	})

	t.Run("minikube", func(t *testing.T) {
		drv, err := newDriver(config{Driver: "minikube"}, plugin.Env{}, "demo-cluster", "/kubeconfig", nil)
		require.NoError(t, err)
		assert.IsType(t, &minikubeDriver{}, drv)
	})

	t.Run("a k3d driver that is misconfigured is an error", func(t *testing.T) {
		_, err := newDriver(config{Driver: "k3d", Workers: map[string]map[string]any{"a": {"x": 1}}},
			plugin.Env{}, "demo-cluster", "/kubeconfig", nil)
		require.ErrorIs(t, err, ErrK3dWorkerSettings)
	})

	t.Run("an unknown driver is an error", func(t *testing.T) {
		_, err := newDriver(config{Driver: "bogus"}, plugin.Env{}, "demo-cluster", "/kubeconfig", nil)
		require.ErrorIs(t, err, ErrUnknownDriver)
	})

	t.Run("no driver is an error", func(t *testing.T) {
		_, err := newDriver(config{}, plugin.Env{}, "demo-cluster", "/kubeconfig", nil)
		require.ErrorIs(t, err, ErrUnknownDriver)
	})
}

// requireDocker skips a test when the docker daemon does not answer.
func requireDocker(t *testing.T) {
	t.Helper()
	if err := dockerClient.Available(t.Context()); err != nil {
		t.Skip("docker is unavailable:", err)
	}
}

// requireKind skips a test when the kind binary does not answer.
func requireKind(t *testing.T) {
	t.Helper()
	if err := kindcmd.New(command.Default).Available(t.Context()); err != nil {
		t.Skip("kind is unavailable:", err)
	}
}

func TestReuseOrCreateCluster(t *testing.T) {
	existing := []string{"demo-cluster-control-plane"}
	created := []string{"demo-cluster-control-plane", "demo-cluster-worker"}

	// driverWith builds a driver whose live cluster has nodes, and whose
	// Create records that it ran.
	driverWith := func(nodes []string, ran *bool) fakeDriver {
		return fakeDriver{
			nodes: func(context.Context) ([]string, error) { return nodes, nil },
			create: func(context.Context, createSpec) ([]string, error) {
				*ran = true
				return created, nil
			},
		}
	}

	t.Run("reuses a live cluster whose marker matches the fingerprint", func(t *testing.T) {
		kubeconfig := filepath.Join(t.TempDir(), "kubeconfig")
		require.NoError(t, os.WriteFile(configMarkerFile(kubeconfig), []byte("fake"), 0o600))
		var ran bool
		out := &capture{}

		got, err := reuseOrCreateCluster(t.Context(), driverWith(existing, &ran), "demo-cluster", kubeconfig, 0, out)
		require.NoError(t, err)
		assert.Equal(t, existing, got)
		assert.False(t, ran, "an unchanged cluster must not be recreated")
		assert.Contains(t, strings.Join(out.stdout, "\n"), "reusing cluster demo-cluster")
	})

	t.Run("recreates a live cluster whose marker differs", func(t *testing.T) {
		kubeconfig := filepath.Join(t.TempDir(), "kubeconfig")
		require.NoError(t, os.WriteFile(configMarkerFile(kubeconfig), []byte("stale"), 0o600))
		var ran bool

		got, err := reuseOrCreateCluster(t.Context(), driverWith(existing, &ran), "demo-cluster", kubeconfig, 0, &capture{})
		require.NoError(t, err)
		assert.Equal(t, created, got)
		assert.True(t, ran)
		marker, err := os.ReadFile(configMarkerFile(kubeconfig))
		require.NoError(t, err)
		assert.Equal(t, "fake", string(marker), "the new fingerprint replaces the stale marker")
	})

	t.Run("creates a cluster when none is live", func(t *testing.T) {
		kubeconfig := filepath.Join(t.TempDir(), "kubeconfig")
		var ran bool

		got, err := reuseOrCreateCluster(t.Context(), driverWith(nil, &ran), "demo-cluster", kubeconfig, 0, &capture{})
		require.NoError(t, err)
		assert.Equal(t, created, got)
		assert.True(t, ran)
	})

	t.Run("a failing node listing is an error", func(t *testing.T) {
		drv := fakeDriver{nodes: func(context.Context) ([]string, error) { return nil, errors.New("list failed") }}

		_, err := reuseOrCreateCluster(t.Context(), drv, "demo-cluster", filepath.Join(t.TempDir(), "kubeconfig"), 0, &capture{})
		require.Error(t, err)
		assert.Contains(t, err.Error(), "check for an existing cluster")
	})

	t.Run("a failing create is an error and writes no marker", func(t *testing.T) {
		kubeconfig := filepath.Join(t.TempDir(), "kubeconfig")
		drv := fakeDriver{create: func(context.Context, createSpec) ([]string, error) { return nil, errors.New("create failed") }}

		_, err := reuseOrCreateCluster(t.Context(), drv, "demo-cluster", kubeconfig, 0, &capture{})
		require.Error(t, err)
		_, statErr := os.Stat(configMarkerFile(kubeconfig))
		assert.True(t, os.IsNotExist(statErr))
	})
}
