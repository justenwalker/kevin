package container

import (
	"context"
	"io"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/justenwalker/kevin/internal/cri"
	"github.com/justenwalker/kevin/internal/docker"
	"github.com/justenwalker/kevin/internal/uerr"
	"github.com/justenwalker/kevin/plugin"
)

func TestSchemaCarriesTheEmbeddedSchema(t *testing.T) {
	schema := Container{}.Schema()

	assert.Contains(t, string(schema), "#Config")
	assert.Contains(t, string(schema), "image!", "image must be required")
}

func TestContainerName(t *testing.T) {
	assert.Equal(t, "kevin-demo-api", containerName("demo", "api"))
}

func TestDecode(t *testing.T) {
	t.Run("applies the same defaults as the schema", func(t *testing.T) {
		cfg, err := decode(nil)
		require.NoError(t, err)

		assert.True(t, cfg.Proxy)
		assert.Equal(t, "30s", cfg.StartTimeout)
	})

	t.Run("reads every field", func(t *testing.T) {
		cfg, err := decode([]byte(`{
			"image": "nginx:alpine",
			"pull": true,
			"cmd": ["nginx"],
			"env": {"A": "1"},
			"ports": ["8080:80"],
			"volumes": ["/a:/b"],
			"proxy": false,
			"egress": ["example.com"],
			"start_timeout": "5s"
		}`))
		require.NoError(t, err)

		assert.Equal(t, "nginx:alpine", cfg.Image)
		assert.True(t, cfg.Pull)
		assert.Equal(t, []string{"nginx"}, cfg.Cmd)
		assert.Equal(t, map[string]string{"A": "1"}, cfg.Env)
		assert.Equal(t, []string{"8080:80"}, cfg.Ports)
		assert.Equal(t, []string{"/a:/b"}, cfg.Volumes)
		assert.False(t, cfg.Proxy)
		assert.Equal(t, []string{"example.com"}, cfg.Egress)
		assert.Equal(t, "5s", cfg.StartTimeout)
	})

	t.Run("reads expose", func(t *testing.T) {
		cfg, err := decode([]byte(`{
			"image": "postgres:16",
			"expose": [
				{"port": 5432, "name": "postgres", "protocol": "tcp", "host_port": 15432},
				{"port": 53, "protocol": "udp"}
			]
		}`))
		require.NoError(t, err)

		require.Len(t, cfg.Expose, 2)
		assert.Equal(t, expose{Port: 5432, Name: "postgres", Protocol: "tcp", HostPort: 15432}, cfg.Expose[0])
		assert.Equal(t, expose{Port: 53, Protocol: "udp"}, cfg.Expose[1])
	})

	t.Run("defaults expose protocol to tcp", func(t *testing.T) {
		cfg, err := decode([]byte(`{"image": "postgres:16", "expose": [{"port": 5432}]}`))
		require.NoError(t, err)

		require.Len(t, cfg.Expose, 1)
		assert.Equal(t, "tcp", cfg.Expose[0].Protocol, "the Go side repeats schema.cue's default for a caller that bypasses CUE")
	})

	t.Run("reports broken JSON", func(t *testing.T) {
		_, err := decode([]byte(`{`))
		require.Error(t, err)
		assert.Contains(t, err.Error(), "decode config")
	})
}

func TestBuildEnv(t *testing.T) {
	proxyEnv := map[string]string{
		"HTTP_PROXY": "http://127.0.0.1:8080",
		"NO_PROXY":   "localhost",
	}

	tests := []struct {
		name   string
		cfg    config
		capath string
		want   map[string]string
	}{
		{
			name: "the proxy variables and the CA arrive",
			cfg:  config{Proxy: true, Env: map[string]string{"APP": "1"}},
			want: map[string]string{
				"APP":           "1",
				"HTTP_PROXY":    "http://127.0.0.1:8080",
				"NO_PROXY":      "localhost",
				"SSL_CERT_FILE": caPath,
			},
			capath: "/home/user/.kevin/root.crt",
		},
		{
			name:   "a step variable wins over a proxy variable",
			cfg:    config{Proxy: true, Env: map[string]string{"HTTP_PROXY": "http://elsewhere"}},
			capath: "",
			want: map[string]string{
				"HTTP_PROXY": "http://elsewhere",
				"NO_PROXY":   "localhost",
			},
		},
		{
			name:   "proxy false keeps the step environment alone",
			cfg:    config{Proxy: false, Env: map[string]string{"APP": "1"}},
			capath: "/home/user/.kevin/root.crt",
			want:   map[string]string{"APP": "1"},
		},
		{
			name:   "no CA means no SSL_CERT_FILE",
			cfg:    config{Proxy: true},
			capath: "",
			want: map[string]string{
				"HTTP_PROXY": "http://127.0.0.1:8080",
				"NO_PROXY":   "localhost",
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := buildEnv(tt.cfg, &plugin.UpRequest{
				Env: plugin.Env{ProxyEnv: proxyEnv, CAPath: tt.capath},
			})
			assert.Equal(t, tt.want, got)
		})
	}

	t.Run("does not change the step config", func(t *testing.T) {
		cfg := config{Proxy: true, Env: map[string]string{"APP": "1"}}

		buildEnv(cfg, &plugin.UpRequest{Env: plugin.Env{ProxyEnv: map[string]string{"HTTP_PROXY": "x"}}})

		assert.Equal(t, map[string]string{"APP": "1"}, cfg.Env, "the config map must stay untouched")
	})
}

func TestOutputs(t *testing.T) {
	t.Run("ip comes from the shared network, not from bridge", func(t *testing.T) {
		info := cri.Container{
			IPs:   map[string]string{"kevin-demo": "172.20.0.3", "bridge": "172.17.0.2"},
			Ports: map[string]string{"80/tcp": "127.0.0.1:8080"},
		}

		got := outputs("9f2c4a", "kevin-demo-api", info, "kevin-demo")

		assert.Equal(t, map[string]string{
			"id":      "9f2c4a",
			"name":    "kevin-demo-api",
			"ip":      "172.20.0.3",
			"host_80": "127.0.0.1:8080",
		}, got, "ip must come from the shared network, not from bridge")
	})

	t.Run("without an address on the shared network", func(t *testing.T) {
		got := outputs("a", "c", cri.Container{IPs: map[string]string{}}, "kevin-demo")

		assert.Equal(t, map[string]string{"id": "a", "name": "c"}, got)
	})
}

func TestTrimProto(t *testing.T) {
	assert.Equal(t, "80", trimProto("80/tcp"))
	assert.Equal(t, "53", trimProto("53/udp"))
	assert.Equal(t, "80", trimProto("80"))
}

func TestPublishSpec(t *testing.T) {
	tests := []struct {
		name string
		e    expose
		want string
	}{
		{name: "ephemeral tcp", e: expose{Port: 5432, Protocol: "tcp"}, want: "127.0.0.1::5432"},
		{name: "pinned tcp", e: expose{Port: 5432, Protocol: "tcp", HostPort: 15432}, want: "127.0.0.1:15432:5432"},
		{name: "ephemeral udp", e: expose{Port: 53, Protocol: "udp"}, want: "127.0.0.1::53/udp"},
		{name: "pinned udp", e: expose{Port: 53, Protocol: "udp", HostPort: 5353}, want: "127.0.0.1:5353:53/udp"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, publishSpec(tt.e))
		})
	}
}

func TestExposedPorts(t *testing.T) {
	t.Run("collects every published port", func(t *testing.T) {
		cfg := config{Expose: []expose{
			{Port: 5432, Name: "postgres", Protocol: "tcp"},
			{Port: 53, Protocol: "udp"},
		}}
		info := cri.Container{Ports: map[string]string{
			"5432/tcp": "127.0.0.1:49001",
			"53/udp":   "127.0.0.1:49002",
		}}

		got, err := exposedPorts(cfg, info)
		require.NoError(t, err)
		require.Len(t, got, 2)
		assert.Equal(t, plugin.ExposedPort{Name: "postgres", Protocol: "tcp", Upstream: "127.0.0.1:49001"}, got[0])
		assert.Equal(t, plugin.ExposedPort{Name: "53", Protocol: "udp", Upstream: "127.0.0.1:49002"}, got[1],
			"an entry with no name defaults to the port number")
	})

	t.Run("reports an unpublished port", func(t *testing.T) {
		cfg := config{Expose: []expose{{Port: 5432, Protocol: "tcp"}}}

		_, err := exposedPorts(cfg, cri.Container{Ports: map[string]string{}})

		require.ErrorIs(t, err, ErrNoPort)
		assert.Contains(t, err.Error(), "5432")
		assert.Equal(t, "the container isn't listening on tcp 5432 - check the image actually exposes it, or fix the expose block",
			uerr.Display(err))
	})
}

type noopEmitter struct{}

func (noopEmitter) Log(string, string)            {}
func (noopEmitter) Progress(string, int64, int64) {}

// requireDocker skips a test when the docker daemon does not answer.
func requireDocker(t *testing.T) {
	t.Helper()
	if err := (docker.Client{}).Available(t.Context()); err != nil {
		t.Skip("docker is unavailable:", err)
	}
}

// testEnv creates a network for one test and returns the environment that a
// step receives. A network alias needs a user-defined network, thus the
// default bridge does not work here.
func testEnv(t *testing.T) plugin.Env {
	t.Helper()
	requireDocker(t)

	env := plugin.Env{
		Project: "kevin-plugin-test",
		Network: "kevin-plugin-test",
		Domain:  "kevin.home",
	}
	client, err := docker.New(nil)
	require.NoError(t, err)
	require.NoError(t, client.NetworkCreate(t.Context(), env.Network, map[string]string{
		cri.LabelProject: env.Project,
	}))
	t.Cleanup(func() {
		_ = client.NetworkRemove(context.WithoutCancel(t.Context()), env.Network)
	})
	return env
}

func TestUp(t *testing.T) {
	t.Run("reports a bad timeout", func(t *testing.T) {
		_, err := Container{}.Up(t.Context(), &plugin.UpRequest{
			Step:   "api",
			Config: []byte(`{"image":"nginx","start_timeout":"soon"}`),
		}, &noopEmitter{})

		require.Error(t, err)
		assert.Contains(t, err.Error(), "start_timeout")
	})

	t.Run("brings up and tears down against docker", func(t *testing.T) {
		env := testEnv(t)
		name := containerName(env.Project, "web")
		t.Cleanup(func() { _ = (docker.Client{}).Remove(context.WithoutCancel(t.Context()), name) })

		result, err := Container{}.Up(t.Context(), &plugin.UpRequest{
			Step:   "web",
			Env:    env,
			Config: []byte(`{"image":"nginx:alpine","expose":[{"port":80,"name":"http"}]}`),
		}, &noopEmitter{})
		require.NoError(t, err)

		assert.Equal(t, name, result.Outputs["name"].Reveal())
		assert.NotEmpty(t, result.Outputs["id"])
		assert.NotEmpty(t, result.Outputs["ip"], "the step must publish its address on the shared network")
		assert.Regexp(t, hostPortPattern, result.Outputs["host_80"],
			"the published port must be a host-reachable address, already accepting connections")

		require.Len(t, result.ExposedPorts, 1)
		require.Len(t, result.Details, 1, "every exposed port must also appear on the card")
		assert.Equal(t, result.ExposedPorts[0].Detail(), result.Details[0])

		info, err := (docker.Client{}).Inspect(t.Context(), name)
		require.NoError(t, err)
		assert.True(t, info.Running)

		// A second Up replaces the container instead of failing on the name.
		_, err = Container{}.Up(t.Context(), &plugin.UpRequest{
			Step:   "web",
			Env:    env,
			Config: []byte(`{"image":"busybox:stable","cmd":["sleep","60"]}`),
		}, &noopEmitter{})
		require.NoError(t, err, "Up must be idempotent")

		require.NoError(t, Container{}.Down(t.Context(), &plugin.DownRequest{Step: "web", Env: env}, &noopEmitter{}))

		_, err = (docker.Client{}).Inspect(t.Context(), name)
		require.ErrorIs(t, err, cri.ErrNotFound)

		// Down is idempotent.
		require.NoError(t, Container{}.Down(t.Context(), &plugin.DownRequest{Step: "web", Env: env}, &noopEmitter{}))
	})

	t.Run("exposes a raw TCP port against docker", func(t *testing.T) {
		env := testEnv(t)
		name := containerName(env.Project, "web")
		t.Cleanup(func() { _ = (docker.Client{}).Remove(context.WithoutCancel(t.Context()), name) })

		result, err := Container{}.Up(t.Context(), &plugin.UpRequest{
			Step:   "web",
			Env:    env,
			Config: []byte(`{"image":"nginx:alpine","expose":[{"port":80,"name":"http"}]}`),
		}, &noopEmitter{})
		require.NoError(t, err)

		require.Len(t, result.ExposedPorts, 1)
		assert.Equal(t, "http", result.ExposedPorts[0].Name)
		assert.Equal(t, "tcp", result.ExposedPorts[0].Protocol)
		assert.Regexp(t, hostPortPattern, result.ExposedPorts[0].Upstream,
			"the expose entry must point at a published port on the host, already accepting connections")

		require.Len(t, result.Details, 1, "the exposed port must also appear on the card")
		assert.Equal(t, plugin.Detail{Label: "tcp http", Value: plugin.String(result.ExposedPorts[0].Upstream), Copyable: true}, result.Details[0])
	})

	t.Run("fails when the container stops during startup", func(t *testing.T) {
		env := testEnv(t)
		name := containerName(env.Project, "boom")
		t.Cleanup(func() { _ = (docker.Client{}).Remove(context.WithoutCancel(t.Context()), name) })

		_, err := Container{}.Up(t.Context(), &plugin.UpRequest{
			Step:   "boom",
			Env:    env,
			Config: []byte(`{"image":"busybox:stable","cmd":["sh","-c","exit 3"],"start_timeout":"20s"}`),
		}, &noopEmitter{})

		require.ErrorIs(t, err, ErrExited)
		assert.Contains(t, err.Error(), "code 3")
	})
}

// fakeRuntime is a hand-written cri.Runtime double. It lets a test drive
// Up/Down's orchestration logic (deadlines, exit codes, call order) without
// a running docker daemon.
type fakeRuntime struct {
	run     func(ctx context.Context, spec cri.RunSpec) (string, error)
	remove  func(ctx context.Context, name string) error
	inspect func(ctx context.Context, name string) (cri.Container, error)
}

func (f fakeRuntime) Run(ctx context.Context, spec cri.RunSpec) (string, error) {
	return f.run(ctx, spec)
}

func (f fakeRuntime) Remove(ctx context.Context, name string) error {
	if f.remove == nil {
		return nil
	}
	return f.remove(ctx, name)
}

func (f fakeRuntime) Inspect(ctx context.Context, name string) (cri.Container, error) {
	return f.inspect(ctx, name)
}

func (fakeRuntime) NetworkCreate(context.Context, string, map[string]string) error { return nil }

func (fakeRuntime) NetworkRemove(context.Context, string) error { return nil }

func (fakeRuntime) NetworkConnect(context.Context, string, string) error { return nil }

func (fakeRuntime) Available(context.Context) error { return nil }

func (fakeRuntime) Exec(context.Context, string, ...string) (string, error) { return "", nil }

func (fakeRuntime) ExecInput(context.Context, string, io.Reader, ...string) (string, error) {
	return "", nil
}

func (fakeRuntime) Save(context.Context, string) (io.ReadCloser, error) {
	return io.NopCloser(strings.NewReader("")), nil
}

// useFakeRuntime substitutes rt for the real engine selection for the
// duration of the test.
func useFakeRuntime(t *testing.T, rt cri.Runtime) {
	t.Helper()
	orig := newRuntime
	newRuntime = func(plugin.Env) (cri.Runtime, error) { return rt, nil }
	t.Cleanup(func() { newRuntime = orig })
}

func TestNewRuntime(t *testing.T) {
	_, err := newRuntime(plugin.Env{Engine: "bogus"})
	require.ErrorIs(t, err, ErrUnsupportedEngine)
	assert.Equal(t, `kevin only supports the docker engine today (got "bogus") - remove engine from kevin.cue, or set it to "docker"`,
		uerr.Display(err))
}

func TestUpWithFakeEngine(t *testing.T) {
	t.Run("removes any leftover container before it runs a new one", func(t *testing.T) {
		var calls []string
		useFakeRuntime(t, fakeRuntime{
			remove: func(context.Context, string) error {
				calls = append(calls, "remove")
				return nil
			},
			run: func(context.Context, cri.RunSpec) (string, error) {
				calls = append(calls, "run")
				return "abc123", nil
			},
			inspect: func(context.Context, string) (cri.Container, error) {
				return cri.Container{Running: true}, nil
			},
		})

		_, err := Container{}.Up(t.Context(), &plugin.UpRequest{
			Step:   "web",
			Config: []byte(`{"image":"nginx"}`),
		}, &noopEmitter{})
		require.NoError(t, err)
		assert.Equal(t, []string{"remove", "run"}, calls, "Up must remove a leftover container before it runs a new one")
	})

	t.Run("propagates a run failure without inspecting", func(t *testing.T) {
		inspected := false
		useFakeRuntime(t, fakeRuntime{
			run: func(context.Context, cri.RunSpec) (string, error) {
				return "", assert.AnError
			},
			inspect: func(context.Context, string) (cri.Container, error) {
				inspected = true
				return cri.Container{}, nil
			},
		})

		_, err := Container{}.Up(t.Context(), &plugin.UpRequest{
			Step:   "web",
			Config: []byte(`{"image":"nginx"}`),
		}, &noopEmitter{})
		require.ErrorIs(t, err, assert.AnError)
		assert.False(t, inspected, "Up must not inspect a container that never ran")
	})

	t.Run("fails when the container exits before it reports running", func(t *testing.T) {
		useFakeRuntime(t, fakeRuntime{
			run: func(context.Context, cri.RunSpec) (string, error) { return "abc123", nil },
			inspect: func(context.Context, string) (cri.Container, error) {
				return cri.Container{Running: false, ExitCode: 3}, nil
			},
		})

		_, err := Container{}.Up(t.Context(), &plugin.UpRequest{
			Step:   "boom",
			Config: []byte(`{"image":"nginx","start_timeout":"5s"}`),
		}, &noopEmitter{})
		require.ErrorIs(t, err, ErrExited)
		assert.Contains(t, err.Error(), "code 3")
	})

	t.Run("gives up once the start_timeout deadline passes", func(t *testing.T) {
		useFakeRuntime(t, fakeRuntime{
			run: func(context.Context, cri.RunSpec) (string, error) { return "abc123", nil },
			inspect: func(context.Context, string) (cri.Container, error) {
				// Never running, never exited: the container is stuck starting up.
				return cri.Container{}, nil
			},
		})

		start := time.Now()
		_, err := Container{}.Up(t.Context(), &plugin.UpRequest{
			Step:   "stuck",
			Config: []byte(`{"image":"nginx","start_timeout":"20ms"}`),
		}, &noopEmitter{})
		require.ErrorIs(t, err, context.DeadlineExceeded)
		assert.Less(t, time.Since(start), 2*time.Second, "Up must give up at the deadline, not hang")
	})

	t.Run("reports an unsupported engine before it touches any runtime", func(t *testing.T) {
		_, err := Container{}.Up(t.Context(), &plugin.UpRequest{
			Step:   "web",
			Env:    plugin.Env{Engine: "bogus"},
			Config: []byte(`{"image":"nginx"}`),
		}, &noopEmitter{})
		require.ErrorIs(t, err, ErrUnsupportedEngine)
	})
}

func TestDownWithFakeEngine(t *testing.T) {
	t.Run("removes the container", func(t *testing.T) {
		var removed string
		useFakeRuntime(t, fakeRuntime{
			remove: func(_ context.Context, name string) error {
				removed = name
				return nil
			},
		})

		err := Container{}.Down(t.Context(), &plugin.DownRequest{
			Step: "web",
			Env:  plugin.Env{Project: "demo"},
		}, &noopEmitter{})
		require.NoError(t, err)
		assert.Equal(t, "kevin-demo-web", removed)
	})

	t.Run("reports an unsupported engine before it touches any runtime", func(t *testing.T) {
		err := Container{}.Down(t.Context(), &plugin.DownRequest{
			Step: "web",
			Env:  plugin.Env{Engine: "bogus"},
		}, &noopEmitter{})
		require.ErrorIs(t, err, ErrUnsupportedEngine)
	})
}

// hostPortPattern matches an address on the loopback with any port.
const hostPortPattern = `^127\.0\.0\.1:[0-9]+$`
