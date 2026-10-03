package clusterrelay

import (
	"context"
	"io"
	"strconv"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/justenwalker/kevin/internal/cri"
	"github.com/justenwalker/kevin/internal/relay"
)

func TestPodManifest(t *testing.T) {
	t.Run("no udp pool adds no udp args or ports", func(t *testing.T) {
		got := PodManifest("kind-example-control-plane", "kevin-relay:dev", 0)

		assert.Contains(t, got, "nodeName: kind-example-control-plane")
		assert.Contains(t, got, "image: kevin-relay:dev")
		assert.Contains(t, got, "imagePullPolicy: Never")
		assert.Contains(t, got, `args: ["socks5-gateway", "--listen", ":1080"]`)
		assert.Contains(t, got, "containerPort: 1080")
		assert.Contains(t, got, "hostPort: 1080")
		assert.NotContains(t, got, "udp-relay-ports")
		assert.NotContains(t, got, "UDP")
	})

	t.Run("a udp pool adds the flag and one ports entry per port", func(t *testing.T) {
		got := PodManifest("kind-example-control-plane", "kevin-relay:dev", 3)

		assert.Contains(t, got, `"--udp-relay-ports", "40000-40002"`)
		assert.Contains(t, got, "containerPort: 40000\n      hostPort: 40000\n      protocol: UDP")
		assert.Contains(t, got, "containerPort: 40001\n      hostPort: 40001\n      protocol: UDP")
		assert.Contains(t, got, "containerPort: 40002\n      hostPort: 40002\n      protocol: UDP")
	})
}

// fakeRuntime is a hand-written cri.Runtime double holding one container
// slot, enough to drive StartForwarder and LookupForwarder.
type fakeRuntime struct {
	container *cri.Container
	runs      []cri.RunSpec
	removes   int
}

var _ cri.Runtime = (*fakeRuntime)(nil)

func (f *fakeRuntime) Inspect(context.Context, string) (cri.Container, error) {
	if f.container == nil {
		return cri.Container{}, cri.ErrNotFound
	}
	return *f.container, nil
}

func (f *fakeRuntime) Remove(context.Context, string) error {
	f.removes++
	f.container = nil
	return nil
}

func (f *fakeRuntime) Run(_ context.Context, spec cri.RunSpec) (string, error) {
	f.runs = append(f.runs, spec)
	ports := map[string]string{"1080/tcp": "127.0.0.1:50000"}
	for i, p := range spec.Ports[1:] {
		container := strings.TrimPrefix(p, "127.0.0.1::")
		ports[container] = "127.0.0.1:" + strconv.Itoa(51000+i)
	}
	f.container = &cri.Container{
		Name: spec.Name, Running: true, Labels: spec.Labels, Ports: ports,
	}
	return "id", nil
}

func (*fakeRuntime) Available(context.Context) error { return nil }

func (*fakeRuntime) Exec(context.Context, string, ...string) (string, error) { return "", nil }

func (*fakeRuntime) ExecInput(context.Context, string, io.Reader, ...string) (string, error) {
	return "", nil
}

func (*fakeRuntime) Build(context.Context, cri.BuildSpec, io.Writer) error { return nil }

func (*fakeRuntime) Save(context.Context, string) (io.ReadCloser, error) {
	return io.NopCloser(strings.NewReader("")), nil
}

func (*fakeRuntime) NetworkCreate(context.Context, string, cri.NetworkOptions) error { return nil }

func (*fakeRuntime) NetworkRemove(context.Context, string) error { return nil }

func (*fakeRuntime) NetworkConnect(context.Context, string, string) error { return nil }

func (*fakeRuntime) NetworkGateway(context.Context, string) (cri.Gateway, error) {
	return cri.Gateway{}, nil
}

func (*fakeRuntime) ListByLabel(context.Context, string, string) ([]string, error) { return nil, nil }

func testSpec(target string) ForwarderSpec {
	return ForwarderSpec{
		Name: ForwarderName("c"), Image: "kevin-relay:dev", Network: "net",
		Target: target, Project: "p", Scope: "env", Step: "cluster",
	}
}

func TestForwarderName(t *testing.T) {
	assert.Equal(t, "kevin-c-relay-fwd", ForwarderName("c"))
}

func TestStartForwarder(t *testing.T) {
	t.Run("publishes the tcp port and one udp port per pool entry", func(t *testing.T) {
		t.Setenv(relay.UDPPoolSizeEnvVar, "2")
		rt := &fakeRuntime{}

		got, err := StartForwarder(t.Context(), rt, testSpec("node-a"))
		require.NoError(t, err)

		require.Len(t, rt.runs, 1)
		assert.Equal(t, []string{"127.0.0.1::1080", "127.0.0.1::40000/udp", "127.0.0.1::40001/udp"}, rt.runs[0].Ports)
		assert.Equal(t, []string{
			"port-forward", "--target", "node-a", "--tcp", "1080", "--udp-relay-ports", "40000-40001",
		}, rt.runs[0].Cmd)
		assert.Equal(t, "net", rt.runs[0].Network)
		assert.Equal(t, "p:env:cluster", rt.runs[0].Labels[cri.LabelURN])
		assert.Equal(t, "127.0.0.1:50000", got.Addr)
		assert.Len(t, got.UDPAddrs, 2)
	})

	t.Run("a disabled pool publishes no udp port", func(t *testing.T) {
		t.Setenv(relay.UDPPoolSizeEnvVar, "0")
		rt := &fakeRuntime{}

		got, err := StartForwarder(t.Context(), rt, testSpec("node-a"))
		require.NoError(t, err)

		assert.Equal(t, []string{"127.0.0.1::1080"}, rt.runs[0].Ports)
		assert.NotContains(t, rt.runs[0].Cmd, "--udp-relay-ports")
		assert.Nil(t, got.UDPAddrs)
	})

	t.Run("reuses a running forwarder with matching labels", func(t *testing.T) {
		t.Setenv(relay.UDPPoolSizeEnvVar, "1")
		rt := &fakeRuntime{}
		first, err := StartForwarder(t.Context(), rt, testSpec("node-a"))
		require.NoError(t, err)

		second, err := StartForwarder(t.Context(), rt, testSpec("node-a"))
		require.NoError(t, err)

		assert.Len(t, rt.runs, 1)
		assert.Equal(t, first, second)
	})

	t.Run("recreates the forwarder when the target changes", func(t *testing.T) {
		t.Setenv(relay.UDPPoolSizeEnvVar, "1")
		rt := &fakeRuntime{}
		_, err := StartForwarder(t.Context(), rt, testSpec("node-a"))
		require.NoError(t, err)

		_, err = StartForwarder(t.Context(), rt, testSpec("node-b"))
		require.NoError(t, err)

		assert.Len(t, rt.runs, 2)
		assert.Equal(t, 2, rt.removes)
	})

	t.Run("an invalid pool size is an error", func(t *testing.T) {
		t.Setenv(relay.UDPPoolSizeEnvVar, "many")

		_, err := StartForwarder(t.Context(), &fakeRuntime{}, testSpec("node-a"))
		require.ErrorIs(t, err, relay.ErrInvalidUDPPoolSize)
	})
}

func TestLookupForwarder(t *testing.T) {
	t.Run("absent reports false", func(t *testing.T) {
		_, ok, err := LookupForwarder(t.Context(), &fakeRuntime{}, "x")
		require.NoError(t, err)
		assert.False(t, ok)
	})

	t.Run("stopped reports false", func(t *testing.T) {
		rt := &fakeRuntime{container: &cri.Container{Running: false}}
		_, ok, err := LookupForwarder(t.Context(), rt, "x")
		require.NoError(t, err)
		assert.False(t, ok)
	})

	t.Run("running reports its addresses", func(t *testing.T) {
		rt := &fakeRuntime{container: &cri.Container{
			Running: true,
			Ports:   map[string]string{"1080/tcp": "127.0.0.1:50000", "40000/udp": "127.0.0.1:50001"},
		}}
		got, ok, err := LookupForwarder(t.Context(), rt, "x")
		require.NoError(t, err)
		assert.True(t, ok)
		assert.Equal(t, "127.0.0.1:50000", got.Addr)
		assert.Equal(t, map[string]string{"40000": "127.0.0.1:50001"}, got.UDPAddrs)
	})

	t.Run("a running container with no socks5 port is an error", func(t *testing.T) {
		rt := &fakeRuntime{container: &cri.Container{Running: true}}
		_, _, err := LookupForwarder(t.Context(), rt, "x")
		require.ErrorIs(t, err, ErrNoPublishedPort)
	})
}
