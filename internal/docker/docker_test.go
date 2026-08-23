package docker

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/netip"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"

	"github.com/justenwalker/kevin/internal/cri"
	"github.com/justenwalker/kevin/internal/uerr"
	"github.com/justenwalker/kevin/protos/pb"
)

func TestFriendlyRunErr(t *testing.T) {
	spec := cri.RunSpec{Name: "web", Image: "acme/widget:latest"}
	tests := []struct {
		name    string
		err     error
		wantMsg string
	}{
		{
			name:    "a port already allocated",
			err:     errors.New(`docker: run "web": Bind for 0.0.0.0:8080 failed: port is already allocated`),
			wantMsg: "a port web needs is already in use on this machine - stop whatever is using it, or change the step's published ports",
		},
		{
			name:    "an address already in use",
			err:     errors.New(`docker: run "web": listen tcp 0.0.0.0:8080: bind: address already in use`),
			wantMsg: "a port web needs is already in use on this machine - stop whatever is using it, or change the step's published ports",
		},
		{
			name:    "a missing image",
			err:     errors.New(`docker: run "web": manifest unknown`),
			wantMsg: `the image "acme/widget:latest" couldn't be found or pulled - check the name and tag, and that you're logged in if it's private`,
		},
		{
			name:    "pull access denied",
			err:     errors.New(`docker: run "web": pull access denied for acme/widget`),
			wantMsg: `the image "acme/widget:latest" couldn't be found or pulled - check the name and tag, and that you're logged in if it's private`,
		},
		{
			name: "an unrecognized failure is left alone",
			err:  errors.New(`docker: run "web": something else went wrong`),
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := friendlyRunErr(tt.err, spec)
			require.ErrorIs(t, got, tt.err)
			if tt.wantMsg == "" {
				assert.Equal(t, tt.err.Error(), uerr.Display(got))
				return
			}
			assert.Equal(t, tt.wantMsg, uerr.Display(got))
		})
	}
}

// requireDocker skips a test when the docker daemon does not answer.
func requireDocker(t *testing.T) {
	t.Helper()
	if err := (Client{}).Available(t.Context()); err != nil {
		t.Skip("docker is unavailable:", err)
	}
}

func TestRunArgs(t *testing.T) {
	t.Run("orders every flag before the image", func(t *testing.T) {
		args := runArgs(cri.RunSpec{
			Image:    "nginx:1",
			Name:     "kevin-demo-api",
			Network:  "kevin-demo",
			Alias:    "api",
			Pull:     true,
			Labels:   map[string]string{cri.LabelStep: "api", cri.LabelProject: "demo"},
			Env:      map[string]string{"B": "2", "A": "1"},
			Ports:    []string{"8080:80"},
			Volumes:  []string{"/ca:/etc/ssl/kevin:ro"},
			DNS:      []string{"172.20.0.1", "127.0.0.11"},
			AddHosts: []string{"web.kevin.home:172.20.0.5", "api.kevin.home:172.20.0.6"},
			Cmd:      []string{"nginx", "-g", "daemon off;"},
		})

		assert.Equal(t, []string{
			"run", "--detach", "--name", "kevin-demo-api",
			"--network", "kevin-demo",
			"--network-alias", "api",
			"--pull", "always",
			"--label", "kevin.project=demo",
			"--label", "kevin.step=api",
			"--env", "A=1",
			"--env", "B=2",
			"--publish", "8080:80",
			"--volume", "/ca:/etc/ssl/kevin:ro",
			"--dns", "172.20.0.1",
			"--dns", "127.0.0.11",
			"--add-host", "web.kevin.home:172.20.0.5",
			"--add-host", "api.kevin.home:172.20.0.6",
			"nginx:1",
			"nginx", "-g", "daemon off;",
		}, args)
	})

	t.Run("overrides the entrypoint, keeping only the first element as the flag", func(t *testing.T) {
		args := runArgs(cri.RunSpec{
			Image:      "amazon/aws-cli",
			Name:       "c",
			Entrypoint: []string{"sh", "-c"},
			Cmd:        []string{"aws s3 ls"},
		})

		assert.Equal(t, []string{
			"run", "--detach", "--name", "c",
			"--entrypoint", "sh",
			"amazon/aws-cli",
			"-c",
			"aws s3 ls",
		}, args)
	})

	t.Run("is stable across calls", func(t *testing.T) {
		// A map has no order. The arguments must not change between two
		// runs, or a diff of the command line becomes noise.
		spec := cri.RunSpec{
			Image:  "busybox",
			Name:   "c",
			Labels: map[string]string{"z": "1", "a": "2", "m": "3"},
			Env:    map[string]string{"Z": "1", "A": "2", "M": "3"},
		}

		want := runArgs(spec)
		for range 20 {
			assert.Equal(t, want, runArgs(spec))
		}
	})

	t.Run("omits what is not set", func(t *testing.T) {
		args := runArgs(cri.RunSpec{Image: "busybox", Name: "c"})

		assert.Equal(t, []string{"run", "--detach", "--name", "c", "busybox"}, args)
		assert.NotContains(t, args, "--network")
		assert.NotContains(t, args, "--pull")
	})
}

// inspectFixture is the shape that `docker inspect --format '{{json .}}'`
// returns, reduced to the fields that kevin reads.
const inspectFixture = `{
  "Id": "9f2c4a",
  "Name": "/kevin-demo-api",
  "State": {"Running": true, "ExitCode": 0},
  "NetworkSettings": {
    "Networks": {
      "kevin-demo": {"IPAddress": "172.20.0.3"},
      "bridge": {"IPAddress": ""}
    },
    "Ports": {
      "80/tcp": [{"HostIp": "0.0.0.0", "HostPort": "32768"}],
      "443/tcp": [{"HostIp": "127.0.0.1", "HostPort": "32769"}],
      "9000/tcp": []
    }
  }
}`

func TestFromInspect(t *testing.T) {
	t.Run("a running container", func(t *testing.T) {
		var raw inspectResult
		require.NoError(t, json.Unmarshal([]byte(inspectFixture), &raw))

		c := fromInspect(raw)

		assert.Equal(t, "9f2c4a", c.ID)
		assert.Equal(t, "kevin-demo-api", c.Name, "the leading slash must go")
		assert.True(t, c.Running)

		assert.Equal(t, map[string]string{"kevin-demo": "172.20.0.3"}, c.IPs,
			"a network without an address must not appear")

		assert.Equal(t, map[string]string{
			"80/tcp":  "127.0.0.1:32768",
			"443/tcp": "127.0.0.1:32769",
		}, c.Ports, "0.0.0.0 must become a usable address, and an unbound port must not appear")
	})

	t.Run("a stopped container", func(t *testing.T) {
		var raw inspectResult
		require.NoError(t, json.Unmarshal([]byte(
			`{"Id":"a","Name":"/c","State":{"Running":false,"ExitCode":137}}`), &raw))

		c := fromInspect(raw)

		assert.False(t, c.Running)
		assert.Equal(t, 137, c.ExitCode)
		assert.Empty(t, c.IPs)
		assert.Empty(t, c.Ports)
	})
}

func TestGatewayFromInspect(t *testing.T) {
	tests := []struct {
		name    string
		out     string
		want    netip.Addr
		wantErr error
	}{
		{name: "a single ipv4 gateway", out: "172.20.0.1 \n", want: netip.MustParseAddr("172.20.0.1")},
		{name: "an ipv4 gateway before an ipv6 gateway", out: "172.20.0.1 fe80::1 ", want: netip.MustParseAddr("172.20.0.1")},
		{name: "an ipv6 gateway before an ipv4 gateway", out: "fe80::1 172.20.0.1 ", want: netip.MustParseAddr("172.20.0.1")},
		{name: "no gateway", out: "", wantErr: cri.ErrNoGateway},
		{name: "only an ipv6 gateway", out: "fe80::1 ", wantErr: cri.ErrNoGateway},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := gatewayFromInspect(tt.out)
			if tt.wantErr != nil {
				require.ErrorIs(t, err, tt.wantErr, "an absent ipv4 gateway must report ErrNoGateway")
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tt.want, got, "the first ipv4 address in the list must win")
		})
	}
}

func TestExec(t *testing.T) {
	t.Run("runs inside a container", func(t *testing.T) {
		requireDocker(t)
		c := Client{}

		name := "kevin-docker-exec-test"
		_, err := c.Run(t.Context(), cri.RunSpec{
			Image: "busybox:stable",
			Name:  name,
			Cmd:   []string{"sh", "-c", "sleep 300"},
		})
		require.NoError(t, err)
		t.Cleanup(func() { _ = c.Remove(context.WithoutCancel(t.Context()), name) })

		out, err := c.Exec(t.Context(), name, "echo", "hello")
		require.NoError(t, err)
		assert.Equal(t, "hello", strings.TrimSpace(out), "Exec must return the standard output of the command")
	})

	t.Run("reports a missing container", func(t *testing.T) {
		requireDocker(t)
		c := Client{}

		_, err := c.Exec(t.Context(), "kevin-docker-exec-test-absent", "echo", "hello")
		require.Error(t, err)
		assert.ErrorIs(t, err, cri.ErrNotFound)
	})
}

func TestExecInput(t *testing.T) {
	t.Run("feeds standard input to the command", func(t *testing.T) {
		requireDocker(t)
		c := Client{}

		name := "kevin-docker-exec-input-test"
		_, err := c.Run(t.Context(), cri.RunSpec{
			Image: "busybox:stable",
			Name:  name,
			Cmd:   []string{"sh", "-c", "sleep 300"},
		})
		require.NoError(t, err)
		t.Cleanup(func() { _ = c.Remove(context.WithoutCancel(t.Context()), name) })

		out, err := c.ExecInput(t.Context(), name, strings.NewReader("hello\n"), "cat")
		require.NoError(t, err)
		assert.Equal(t, "hello", strings.TrimSpace(out), "ExecInput must feed stdin to the command")
	})
}

func TestNetworkConnect(t *testing.T) {
	requireDocker(t)
	c := Client{}

	network := "kevin-docker-network-connect-test"
	require.NoError(t, c.NetworkCreate(t.Context(), network, nil))
	t.Cleanup(func() { _ = c.NetworkRemove(context.WithoutCancel(t.Context()), network) })

	name := "kevin-docker-network-connect-test-container"
	_, err := c.Run(t.Context(), cri.RunSpec{
		Image: "busybox:stable",
		Name:  name,
		Cmd:   []string{"sh", "-c", "sleep 300"},
	})
	require.NoError(t, err)
	t.Cleanup(func() { _ = c.Remove(context.WithoutCancel(t.Context()), name) })

	require.NoError(t, c.NetworkConnect(t.Context(), network, name))
	require.NoError(t, c.NetworkConnect(t.Context(), network, name), "NetworkConnect must be idempotent")

	info, err := c.Inspect(t.Context(), name)
	require.NoError(t, err)
	assert.Contains(t, info.IPs, network, "the container must carry an address on the connected network")
}

func TestNewClient(t *testing.T) {
	t.Run("empty config decodes to the zero message", func(t *testing.T) {
		c, err := New(nil)
		require.NoError(t, err)
		assert.Equal(t, Client{}, c)
	})

	t.Run("decodes a valid DockerEngineConfig", func(t *testing.T) {
		b, err := proto.Marshal(&pb.DockerEngineConfig{})
		require.NoError(t, err)

		_, err = New(b)
		require.NoError(t, err)
	})

	t.Run("reports bytes that are not a valid message", func(t *testing.T) {
		_, err := New([]byte{0x00})
		require.Error(t, err)
	})
}

func TestNetworkGateway(t *testing.T) {
	requireDocker(t)
	c := Client{}

	t.Run("returns the network's ipv4 gateway", func(t *testing.T) {
		network := "kevin-docker-network-gateway-test"
		require.NoError(t, c.NetworkCreate(t.Context(), network, nil))
		t.Cleanup(func() { _ = c.NetworkRemove(context.WithoutCancel(t.Context()), network) })

		gw, err := c.NetworkGateway(t.Context(), network)
		require.NoError(t, err)
		assert.True(t, gw.Is4(), "the gateway must be an ipv4 address")
	})

	t.Run("reports ErrNotFound for a missing network", func(t *testing.T) {
		_, err := c.NetworkGateway(t.Context(), "kevin-docker-network-gateway-test-absent")
		assert.ErrorIs(t, err, cri.ErrNotFound)
	})
}

func TestListByLabel(t *testing.T) {
	requireDocker(t)
	c := Client{}

	name := "kevin-docker-list-by-label-test"
	_, err := c.Run(t.Context(), cri.RunSpec{
		Image:  "busybox:stable",
		Name:   name,
		Cmd:    []string{"sh", "-c", "sleep 300"},
		Labels: map[string]string{"kevin.list-test": "yes"},
	})
	require.NoError(t, err)
	t.Cleanup(func() { _ = c.Remove(context.WithoutCancel(t.Context()), name) })

	names, err := c.ListByLabel(t.Context(), "kevin.list-test", "yes")
	require.NoError(t, err)
	assert.Contains(t, names, name)

	names, err = c.ListByLabel(t.Context(), "kevin.list-test", "no-such-value")
	require.NoError(t, err)
	assert.NotContains(t, names, name)
}

func TestSave(t *testing.T) {
	requireDocker(t)
	c := Client{}

	rc, err := c.Save(t.Context(), "busybox:stable")
	require.NoError(t, err)

	data, err := io.ReadAll(rc)
	require.NoError(t, err)
	assert.NotEmpty(t, data, "Save must stream a non-empty tar archive")
	require.NoError(t, rc.Close())
}
