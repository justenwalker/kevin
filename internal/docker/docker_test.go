package docker

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/netip"
	"os/exec"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"

	"github.com/justenwalker/kevin/internal/command/commandtest"
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

func TestRunArgs(t *testing.T) {
	t.Run("orders every flag before the image", func(t *testing.T) {
		args := runArgs(cri.RunSpec{
			Image:    "nginx:1",
			Name:     "kevin-demo-api",
			Network:  "kevin-demo",
			Alias:    "api",
			Pull:     true,
			Labels:   map[string]string{cri.LabelURN: "demo:env:api", cri.LabelProject: "demo"},
			Env:      map[string]string{"B": "2", "A": "1"},
			Ports:    []string{"8080:80"},
			Volumes:  []string{"/ca:/etc/ssl/kevin:ro"},
			DNS:      []string{"172.20.0.1", "127.0.0.11"},
			AddHosts: []string{"web.kevin.home:172.20.0.5", "api.kevin.home:172.20.0.6"},
			CapAdd:   []string{"NET_ADMIN", "SYS_ADMIN"},
			PidHost:  true,
			Cmd:      []string{"nginx", "-g", "daemon off;"},
		})

		assert.Equal(t, []string{
			"run", "--detach", "--name", "kevin-demo-api",
			"--network", "kevin-demo",
			"--network-alias", "api",
			"--pull", "always",
			"--label", "kevin.project=demo",
			"--label", "kevin.urn=demo:env:api",
			"--env", "A=1",
			"--env", "B=2",
			"--publish", "8080:80",
			"--volume", "/ca:/etc/ssl/kevin:ro",
			"--dns", "172.20.0.1",
			"--dns", "127.0.0.11",
			"--add-host", "web.kevin.home:172.20.0.5",
			"--add-host", "api.kevin.home:172.20.0.6",
			"--cap-add", "NET_ADMIN",
			"--cap-add", "SYS_ADMIN",
			"--pid", "host",
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

	t.Run("sets user, workdir, and limits", func(t *testing.T) {
		args := runArgs(cri.RunSpec{
			Image:   "busybox",
			Name:    "c",
			User:    "1000:1000",
			Workdir: "/app",
			CPUs:    "1.5",
			Memory:  "512m",
		})

		assert.Equal(t, []string{
			"run", "--detach", "--name", "c",
			"--user", "1000:1000",
			"--workdir", "/app",
			"--cpus", "1.5",
			"--memory", "512m",
			"busybox",
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
		assert.NotContains(t, args, "--pid")
	})
}

// inspectFixture is the shape that `docker inspect --format '{{json .}}'`
// returns, reduced to the fields that kevin reads.
const inspectFixture = `{
  "Id": "9f2c4a",
  "Name": "/kevin-demo-api",
  "State": {"Running": true, "ExitCode": 0, "Pid": 4242},
  "NetworkSettings": {
    "Networks": {
      "kevin-demo": {"IPAddress": "172.20.0.3", "GlobalIPv6Address": "fd00::3"},
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
		assert.Equal(t, map[string]string{"kevin-demo": "fd00::3"}, c.IPv6,
			"a network without an ipv6 address must not appear")
		assert.Equal(t, "/proc/4242/ns/net", c.NetnsPath)

		assert.Equal(t, map[string]string{
			"80/tcp":  "127.0.0.1:32768",
			"443/tcp": "127.0.0.1:32769",
		}, c.Ports, "0.0.0.0 must become a usable address, and an unbound port must not appear")
	})

	t.Run("a stopped container", func(t *testing.T) {
		var raw inspectResult
		require.NoError(t, json.Unmarshal([]byte(
			`{"Id":"a","Name":"/c","State":{"Running":false,"Status":"exited","ExitCode":137}}`), &raw))

		c := fromInspect(raw)

		assert.False(t, c.Running)
		assert.True(t, c.Exited)
		assert.Equal(t, 137, c.ExitCode)
		assert.Empty(t, c.IPs)
		assert.Empty(t, c.IPv6)
		assert.Empty(t, c.Ports)
		assert.Empty(t, c.NetnsPath, "no pid means no namespace to reach")
	})
}

func TestGatewayFromInspect(t *testing.T) {
	tests := []struct {
		name    string
		out     string
		want    cri.Gateway
		wantErr error
	}{
		{name: "a single ipv4 gateway", out: "172.20.0.1 \n", want: cri.Gateway{V4: netip.MustParseAddr("172.20.0.1")}},
		{
			name: "an ipv4 gateway before an ipv6 gateway", out: "172.20.0.1 fe80::1 ",
			want: cri.Gateway{V4: netip.MustParseAddr("172.20.0.1"), V6: netip.MustParseAddr("fe80::1")},
		},
		{
			name: "an ipv6 gateway before an ipv4 gateway", out: "fe80::1 172.20.0.1 ",
			want: cri.Gateway{V4: netip.MustParseAddr("172.20.0.1"), V6: netip.MustParseAddr("fe80::1")},
		},
		{name: "no gateway", out: "", wantErr: cri.ErrNoGateway},
		{name: "only an ipv6 gateway", out: "fe80::1 ", want: cri.Gateway{V6: netip.MustParseAddr("fe80::1")}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := gatewayFromInspect(tt.out)
			if tt.wantErr != nil {
				require.ErrorIs(t, err, tt.wantErr, "no gateway in either family must report ErrNoGateway")
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tt.want, got, "the first address of each family must win")
		})
	}
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

func TestDefaultRouteArgs(t *testing.T) {
	t.Run("routes through the ipv4 gateway", func(t *testing.T) {
		gw := cri.Gateway{V4: netip.MustParseAddr("192.168.1.1"), V6: netip.MustParseAddr("fd00::1")}

		assert.Equal(t, []string{"ip", "route", "replace", "default", "via", "192.168.1.1"}, defaultRouteArgs(gw))
	})

	t.Run("an ipv6-only gateway has no route to set", func(t *testing.T) {
		assert.Nil(t, defaultRouteArgs(cri.Gateway{V6: netip.MustParseAddr("fd00::1")}))
	})

	t.Run("no gateway has no route to set", func(t *testing.T) {
		assert.Nil(t, defaultRouteArgs(cri.Gateway{}))
	})
}

func TestNetworkCreateArgs(t *testing.T) {
	labels := map[string]string{"kevin.project": "demo"}

	t.Run("without IPv6 the network is IPv4-only, whatever the daemon defaults to", func(t *testing.T) {
		got := networkCreateArgs("kevin-demo", cri.NetworkOptions{Labels: labels})

		assert.Equal(t, []string{"network", "create", "--ipv6=false", "--label", "kevin.project=demo", "kevin-demo"}, got)
	})

	t.Run("with IPv6 the network is dual-stack", func(t *testing.T) {
		got := networkCreateArgs("kevin-demo", cri.NetworkOptions{IPv6: true, Labels: labels})

		assert.Equal(t, []string{"network", "create", "--ipv6", "--label", "kevin.project=demo", "kevin-demo"}, got)
	})
}

func TestBuildArgs(t *testing.T) {
	t.Run("orders every flag before the context", func(t *testing.T) {
		args := buildArgs(cri.BuildSpec{
			Context:    "/proj/api",
			Dockerfile: "/proj/api/Dockerfile.dev",
			Tag:        "kevin-demo-api:latest",
			Args:       map[string]string{"B": "2", "A": "1"},
			Target:     "dev",
			Labels:     map[string]string{cri.LabelURN: "demo:env:api", cri.LabelProject: "demo"},
		})

		assert.Equal(t, []string{
			"build", "--tag", "kevin-demo-api:latest",
			"--file", "/proj/api/Dockerfile.dev",
			"--target", "dev",
			"--label", "kevin.project=demo",
			"--label", "kevin.urn=demo:env:api",
			"--build-arg", "A=1",
			"--build-arg", "B=2",
			"/proj/api",
		}, args)
	})

	t.Run("omits what is not set", func(t *testing.T) {
		args := buildArgs(cri.BuildSpec{Context: ".", Tag: "t"})

		assert.Equal(t, []string{"build", "--tag", "t", "."}, args)
	})
}

// reply answers one Run call: it prints stdout, prints stderr, and returns
// err. check, if set, sees the cmd first.
func reply(stdout, stderr string, err error, check func(cmd *exec.Cmd)) func(context.Context, *exec.Cmd) error {
	return func(_ context.Context, cmd *exec.Cmd) error {
		if check != nil {
			check(cmd)
		}
		if cmd.Stdout != nil {
			_, _ = io.WriteString(cmd.Stdout, stdout)
		}
		if cmd.Stderr != nil {
			_, _ = io.WriteString(cmd.Stderr, stderr)
		}
		return err
	}
}

func TestClientBuild(t *testing.T) {
	t.Run("streams both output streams to the writer", func(t *testing.T) {
		runner := commandtest.NewMockRunner(t)
		runner.EXPECT().Run(mock.Anything, mock.Anything).RunAndReturn(
			func(_ context.Context, cmd *exec.Cmd) error {
				assert.Equal(t, "docker", cmd.Args[0])
				assert.Equal(t, "build", cmd.Args[1])
				_, _ = io.WriteString(cmd.Stdout, "out\n")
				_, _ = io.WriteString(cmd.Stderr, "err\n")
				return nil
			})

		var out strings.Builder
		err := Client{Runner: runner}.Build(t.Context(), cri.BuildSpec{Context: ".", Tag: "t"}, &out)
		require.NoError(t, err)
		assert.Equal(t, "out\nerr\n", out.String())
	})

	t.Run("names the tag when the build fails", func(t *testing.T) {
		runner := commandtest.NewMockRunner(t)
		runner.EXPECT().Run(mock.Anything, mock.Anything).Return(errors.New("exit status 1"))

		err := Client{Runner: runner}.Build(t.Context(), cri.BuildSpec{Context: ".", Tag: "t"}, io.Discard)
		require.Error(t, err)
		assert.Contains(t, err.Error(), `docker: build "t"`)
	})
}

func TestClientRunner(t *testing.T) {
	t.Run("Run returns the trimmed container id", func(t *testing.T) {
		runner := commandtest.NewMockRunner(t)
		runner.EXPECT().Run(mock.Anything, mock.Anything).RunAndReturn(
			reply("abc123\n", "", nil, func(cmd *exec.Cmd) {
				assert.Equal(t, "docker", cmd.Args[0])
				assert.Contains(t, cmd.Args, "busybox:stable")
			}))

		id, err := Client{Runner: runner}.Run(t.Context(), cri.RunSpec{Image: "busybox:stable", Name: "x"})
		require.NoError(t, err)
		assert.Equal(t, "abc123", id)
	})

	t.Run("Run explains a port that is already in use", func(t *testing.T) {
		runner := commandtest.NewMockRunner(t)
		runner.EXPECT().Run(mock.Anything, mock.Anything).RunAndReturn(
			reply("", "port is already allocated\n", errors.New("exit status 125"), nil))

		_, err := Client{Runner: runner}.Run(t.Context(), cri.RunSpec{Image: "busybox:stable", Name: "x"})
		require.Error(t, err)
		assert.Contains(t, uerr.Display(err), "already in use")
	})

	t.Run("Remove ignores a container that is already gone", func(t *testing.T) {
		runner := commandtest.NewMockRunner(t)
		runner.EXPECT().Run(mock.Anything, mock.Anything).RunAndReturn(
			reply("", "No such container\n", errors.New("exit status 1"), nil)).Once()
		runner.EXPECT().Run(mock.Anything, mock.Anything).RunAndReturn(reply("", "", nil, nil)).Once()

		require.NoError(t, Client{Runner: runner}.Remove(t.Context(), "x"))
	})

	t.Run("Remove reports a failure while the container still exists", func(t *testing.T) {
		runner := commandtest.NewMockRunner(t)
		runner.EXPECT().Run(mock.Anything, mock.Anything).RunAndReturn(
			reply("", "", errors.New("exit status 1"), nil)).Once()
		runner.EXPECT().Run(mock.Anything, mock.Anything).RunAndReturn(reply("x\n", "", nil, nil)).Once()

		require.ErrorContains(t, Client{Runner: runner}.Remove(t.Context(), "x"), `docker: remove "x"`)
	})

	t.Run("Exec reports ErrNotFound for a missing container", func(t *testing.T) {
		runner := commandtest.NewMockRunner(t)
		runner.EXPECT().Run(mock.Anything, mock.Anything).RunAndReturn(
			reply("", "", errors.New("exit status 1"), nil)).Once()
		runner.EXPECT().Run(mock.Anything, mock.Anything).RunAndReturn(reply("", "", nil, nil)).Once()

		_, err := Client{Runner: runner}.Exec(t.Context(), "x", "echo")
		require.ErrorIs(t, err, cri.ErrNotFound)
	})

	t.Run("ExecInput passes -i and feeds stdin", func(t *testing.T) {
		runner := commandtest.NewMockRunner(t)
		runner.EXPECT().Run(mock.Anything, mock.Anything).RunAndReturn(
			reply("hello\n", "", nil, func(cmd *exec.Cmd) {
				assert.Equal(t, []string{"docker", "exec", "-i", "x", "cat"}, cmd.Args)
				in, err := io.ReadAll(cmd.Stdin)
				require.NoError(t, err)
				assert.Equal(t, "hello", string(in))
			}))

		out, err := Client{Runner: runner}.ExecInput(t.Context(), "x", strings.NewReader("hello"), "cat")
		require.NoError(t, err)
		assert.Equal(t, "hello\n", out)
	})

	t.Run("ListByLabel splits the names", func(t *testing.T) {
		runner := commandtest.NewMockRunner(t)
		runner.EXPECT().Run(mock.Anything, mock.Anything).RunAndReturn(
			reply("a\nb\n", "", nil, func(cmd *exec.Cmd) {
				assert.Contains(t, cmd.Args, "label=k=v")
			}))

		got, err := Client{Runner: runner}.ListByLabel(t.Context(), "k", "v")
		require.NoError(t, err)
		assert.Equal(t, []string{"a", "b"}, got)
	})

	t.Run("Save streams the archive and Close waits for the process", func(t *testing.T) {
		runner := commandtest.NewMockRunner(t)
		runner.EXPECT().Run(mock.Anything, mock.Anything).RunAndReturn(
			reply("tar-bytes", "", nil, func(cmd *exec.Cmd) {
				assert.Equal(t, []string{"docker", "save", "img:1"}, cmd.Args)
			}))

		rc, err := Client{Runner: runner}.Save(t.Context(), "img:1")
		require.NoError(t, err)
		data, err := io.ReadAll(rc)
		require.NoError(t, err)
		assert.Equal(t, "tar-bytes", string(data))
		require.NoError(t, rc.Close())
	})

	t.Run("Save reports the process failure to the reader", func(t *testing.T) {
		runner := commandtest.NewMockRunner(t)
		runner.EXPECT().Run(mock.Anything, mock.Anything).RunAndReturn(
			reply("", "", errors.New("exit status 1"), nil))

		rc, err := Client{Runner: runner}.Save(t.Context(), "img:1")
		require.NoError(t, err)
		_, err = io.ReadAll(rc)
		require.ErrorContains(t, err, "exit status 1")
		_ = rc.Close()
	})
}
