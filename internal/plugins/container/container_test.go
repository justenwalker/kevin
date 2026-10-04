package container

import (
	"context"
	"io"
	"strings"
	"testing"
	"time"

	"cuelang.org/go/cue"
	"cuelang.org/go/cue/cuecontext"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/justenwalker/kevin/internal/cri"
	"github.com/justenwalker/kevin/internal/podman"
	"github.com/justenwalker/kevin/internal/uerr"
	"github.com/justenwalker/kevin/plugin"
)

func TestSchemaCarriesTheEmbeddedSchema(t *testing.T) {
	schema := Container{}.Schema()

	assert.Contains(t, string(schema), "#Config")
}

func TestSchemaValidation(t *testing.T) {
	ctx := cuecontext.New()
	v := ctx.CompileBytes(Container{}.Schema(), cue.Filename("container/schema.cue"))
	require.NoError(t, v.Err())
	config := v.LookupPath(cue.ParsePath("#Config"))

	validate := func(with string) error {
		return config.Unify(ctx.CompileString(with)).Validate(cue.Concrete(true))
	}

	t.Run("accepts image alone", func(t *testing.T) {
		require.NoError(t, validate(`{image: "nginx"}`))
	})

	t.Run("accepts build alone", func(t *testing.T) {
		require.NoError(t, validate(`{build: {context: "./api"}}`))
	})

	t.Run("accepts pull with image", func(t *testing.T) {
		require.NoError(t, validate(`{image: "nginx", pull: true}`))
	})

	t.Run("rejects image with build", func(t *testing.T) {
		require.Error(t, validate(`{image: "nginx", build: {context: "."}}`))
	})

	t.Run("rejects neither image nor build", func(t *testing.T) {
		require.Error(t, validate(`{cmd: ["true"]}`))
	})

	t.Run("rejects pull with build", func(t *testing.T) {
		require.Error(t, validate(`{build: {context: "."}, pull: true}`))
	})

	t.Run("rejects a build with no context", func(t *testing.T) {
		require.Error(t, validate(`{build: {target: "dev"}}`))
	})

	t.Run("checks the cpus and memory formats", func(t *testing.T) {
		require.NoError(t, validate(`{image: "nginx", cpus: "1.5", memory: "512m"}`))
		require.Error(t, validate(`{image: "nginx", cpus: "lots"}`))
		require.Error(t, validate(`{image: "nginx", memory: "512mb"}`))
	})
}

func TestContainerName(t *testing.T) {
	t.Run("starts with the project and step", func(t *testing.T) {
		assert.Regexp(t, `^kevin-demo-api-[0-9a-f]{8}$`, containerName("demo", "api"))
	})

	t.Run("differs when only the hyphen split moves", func(t *testing.T) {
		assert.NotEqual(t, containerName("a", "b-c"), containerName("a-b", "c"))
	})
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

	t.Run("reads user, workdir, and limits", func(t *testing.T) {
		cfg, err := decode([]byte(`{
			"image": "nginx:alpine",
			"user": "1000:1000",
			"workdir": "/app",
			"cpus": "1.5",
			"memory": "512m"
		}`))
		require.NoError(t, err)

		assert.Equal(t, "1000:1000", cfg.User)
		assert.Equal(t, "/app", cfg.Workdir)
		assert.Equal(t, "1.5", cfg.CPUs)
		assert.Equal(t, "512m", cfg.Memory)
	})

	t.Run("reads expose", func(t *testing.T) {
		cfg, err := decode([]byte(`{
			"image": "postgres:16",
			"expose": {
				"postgres": {"port": 5432, "protocol": "tcp", "host_port": 15432},
				"dns": {"port": 53, "protocol": "udp"}
			}
		}`))
		require.NoError(t, err)

		require.Len(t, cfg.Expose, 2)
		assert.Equal(t, expose{Port: 5432, Protocol: "tcp", HostPort: 15432}, cfg.Expose["postgres"])
		assert.Equal(t, expose{Port: 53, Protocol: "udp"}, cfg.Expose["dns"])
	})

	t.Run("defaults expose protocol to tcp", func(t *testing.T) {
		cfg, err := decode([]byte(`{"image": "postgres:16", "expose": {"postgres": {"port": 5432}}}`))
		require.NoError(t, err)

		require.Len(t, cfg.Expose, 1)
		assert.Equal(t, "tcp", cfg.Expose["postgres"].Protocol, "the Go side repeats schema.cue's default for a caller that bypasses CUE")
	})

	t.Run("reports broken JSON", func(t *testing.T) {
		_, err := decode([]byte(`{`))
		require.Error(t, err)
		assert.Contains(t, err.Error(), "decode config")
	})

	t.Run("accepts relay combined with udp", func(t *testing.T) {
		cfg, err := decode([]byte(`{"image": "postgres:16", "expose": {"dns": {"port": 53, "protocol": "udp", "relay": true}}}`))
		require.NoError(t, err)
		assert.True(t, cfg.Expose["dns"].Relay)
		assert.Equal(t, "udp", cfg.Expose["dns"].Protocol)
	})
}

func TestBuildEnv(t *testing.T) {
	tests := []struct {
		name   string
		cfg    config
		capath string
		want   map[string]string
	}{
		{
			name:   "the CA arrives",
			cfg:    config{Proxy: true, Env: map[string]string{"APP": "1"}},
			capath: "/home/user/.kevin/root.crt",
			want:   map[string]string{"APP": "1", "SSL_CERT_FILE": caPath},
		},
		{
			name:   "a step variable wins over SSL_CERT_FILE",
			cfg:    config{Proxy: true, Env: map[string]string{"SSL_CERT_FILE": "/elsewhere"}},
			capath: "/home/user/.kevin/root.crt",
			want:   map[string]string{"SSL_CERT_FILE": "/elsewhere"},
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
			want:   map[string]string{},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := buildEnv(tt.cfg, &plugin.UpRequest{Env: plugin.Env{CAPath: tt.capath}})
			assert.Equal(t, tt.want, got)
		})
	}

	t.Run("does not change the step config", func(t *testing.T) {
		cfg := config{Proxy: true, Env: map[string]string{"APP": "1"}}

		buildEnv(cfg, &plugin.UpRequest{Env: plugin.Env{CAPath: "/home/user/.kevin/root.crt"}})

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
	t.Run("collects every published port, sorted by name", func(t *testing.T) {
		cfg := config{Expose: map[string]expose{
			"postgres": {Port: 5432, Protocol: "tcp"},
			"dns":      {Port: 53, Protocol: "udp"},
		}}
		info := cri.Container{Ports: map[string]string{
			"5432/tcp": "127.0.0.1:49001",
			"53/udp":   "127.0.0.1:49002",
		}}

		got, err := exposedPorts(cfg, info, "db", "", nil)
		require.NoError(t, err)
		require.Len(t, got, 2)
		assert.Equal(t, plugin.ExposedPort{Name: "dns", Protocol: "udp", Upstream: "127.0.0.1:49002"}, got[0])
		assert.Equal(t, plugin.ExposedPort{Name: "postgres", Protocol: "tcp", Upstream: "127.0.0.1:49001"}, got[1])
	})

	t.Run("reports an unpublished port", func(t *testing.T) {
		cfg := config{Expose: map[string]expose{"postgres": {Port: 5432, Protocol: "tcp"}}}

		_, err := exposedPorts(cfg, cri.Container{Ports: map[string]string{}}, "db", "", nil)

		require.ErrorIs(t, err, ErrNoPort)
		assert.Contains(t, err.Error(), "5432")
		assert.Equal(t, "the container isn't listening on tcp 5432 - check the image actually exposes it, or fix the expose block",
			uerr.Display(err))
	})

	t.Run("builds a socks5 upstream for a relay entry", func(t *testing.T) {
		cfg := config{Expose: map[string]expose{"postgres": {Port: 5432, Protocol: "tcp", Relay: true}}}

		got, err := exposedPorts(cfg, cri.Container{}, "db", "127.0.0.1:54321", nil)
		require.NoError(t, err)
		require.Len(t, got, 1)
		assert.Equal(t, plugin.ExposedPort{Name: "postgres", Protocol: "tcp", Relay: true, Upstream: "socks5://127.0.0.1:54321/db:5432"}, got[0])
	})

	t.Run("reports a relay entry with no relay address", func(t *testing.T) {
		cfg := config{Expose: map[string]expose{"postgres": {Port: 5432, Protocol: "tcp", Relay: true}}}

		_, err := exposedPorts(cfg, cri.Container{}, "db", "", nil)

		require.ErrorIs(t, err, ErrNoRelay)
	})

	t.Run("builds a relay+udp entry with the relay's pool addresses", func(t *testing.T) {
		cfg := config{Expose: map[string]expose{"dns": {Port: 53, Protocol: "udp", Relay: true}}}
		udpAddrs := map[string]string{"40000": "127.0.0.1:41000"}

		got, err := exposedPorts(cfg, cri.Container{}, "db", "127.0.0.1:54321", udpAddrs)
		require.NoError(t, err)
		require.Len(t, got, 1)
		assert.Equal(t, plugin.ExposedPort{
			Name: "dns", Protocol: "udp", Relay: true,
			Upstream: "socks5://127.0.0.1:54321/db:53", RelayUDPAddrs: udpAddrs,
		}, got[0])
	})

	t.Run("reports a relay+udp entry with no udp pool", func(t *testing.T) {
		cfg := config{Expose: map[string]expose{"dns": {Port: 53, Protocol: "udp", Relay: true}}}

		_, err := exposedPorts(cfg, cri.Container{}, "db", "127.0.0.1:54321", nil)

		require.ErrorIs(t, err, ErrNoRelayUDPPool)
	})
}

func TestBuildPorts(t *testing.T) {
	cfg := config{
		Ports: []string{"8080:80"},
		Expose: map[string]expose{
			"postgres": {Port: 5432, Protocol: "tcp"},
			"cache":    {Port: 6379, Protocol: "tcp", Relay: true},
		},
	}

	got := buildPorts(cfg)

	assert.Equal(t, []string{"8080:80", "127.0.0.1::5432"}, got, "a relay entry never gets a docker --publish spec")
}

type noopEmitter struct{}

func (noopEmitter) Log(string, string)            {}
func (noopEmitter) Progress(string, int64, int64) {}

// fakeRuntime is a hand-written cri.Runtime double. It lets a test drive
// Up/Down's orchestration logic (deadlines, exit codes, call order) without
// a running docker daemon.
type fakeRuntime struct {
	run     func(ctx context.Context, spec cri.RunSpec) (string, error)
	remove  func(ctx context.Context, name string) error
	inspect func(ctx context.Context, name string) (cri.Container, error)
	build   func(ctx context.Context, spec cri.BuildSpec, out io.Writer) error
}

func (f fakeRuntime) Build(ctx context.Context, spec cri.BuildSpec, out io.Writer) error {
	if f.build == nil {
		return nil
	}
	return f.build(ctx, spec, out)
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

func (fakeRuntime) NetworkCreate(context.Context, string, cri.NetworkOptions) error { return nil }

func (fakeRuntime) NetworkRemove(context.Context, string) error { return nil }

func (fakeRuntime) NetworkConnect(context.Context, string, string) error { return nil }

func (fakeRuntime) NetworkGateway(context.Context, string) (cri.Gateway, error) {
	return cri.Gateway{}, nil
}

func (fakeRuntime) ListByLabel(context.Context, string, string) ([]string, error) { return nil, nil }

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
	t.Run("an unsupported engine", func(t *testing.T) {
		_, err := newRuntime(plugin.Env{Engine: "bogus"})
		require.ErrorIs(t, err, ErrUnsupportedEngine)
		assert.Equal(t, `kevin doesn't support engine "bogus" - pass --engine or KEVIN_ENGINE as "docker" or "podman"`,
			uerr.Display(err))
	})

	t.Run("podman resolves to the podman runtime", func(t *testing.T) {
		rt, err := newRuntime(plugin.Env{Engine: "podman"})
		require.NoError(t, err)
		assert.IsType(t, podman.Client{}, rt)
	})
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
				return cri.Container{Exited: true, ExitCode: 3}, nil
			},
		})

		_, err := Container{}.Up(t.Context(), &plugin.UpRequest{
			Step:   "boom",
			Config: []byte(`{"image":"nginx","start_timeout":"5s"}`),
		}, &noopEmitter{})
		require.ErrorIs(t, err, ErrExited)
		assert.Contains(t, err.Error(), "code 3")
	})

	t.Run("fails when the container exits with code 0", func(t *testing.T) {
		useFakeRuntime(t, fakeRuntime{
			run: func(context.Context, cri.RunSpec) (string, error) { return "abc123", nil },
			inspect: func(context.Context, string) (cri.Container, error) {
				return cri.Container{Exited: true}, nil
			},
		})

		_, err := Container{}.Up(t.Context(), &plugin.UpRequest{
			Step:   "done",
			Config: []byte(`{"image":"nginx","start_timeout":"5s"}`),
		}, &noopEmitter{})
		require.ErrorIs(t, err, ErrExited)
		assert.Contains(t, err.Error(), "code 0")
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

	t.Run("passes user, workdir, and limits to the engine", func(t *testing.T) {
		var gotSpec cri.RunSpec
		useFakeRuntime(t, fakeRuntime{
			run: func(_ context.Context, spec cri.RunSpec) (string, error) {
				gotSpec = spec
				return "abc123", nil
			},
			inspect: func(context.Context, string) (cri.Container, error) {
				return cri.Container{Running: true}, nil
			},
		})

		_, err := Container{}.Up(t.Context(), &plugin.UpRequest{
			Step:   "api",
			Config: []byte(`{"image":"nginx","user":"1000:1000","workdir":"/app","cpus":"1.5","memory":"512m"}`),
		}, &noopEmitter{})
		require.NoError(t, err)

		assert.Equal(t, "1000:1000", gotSpec.User)
		assert.Equal(t, "/app", gotSpec.Workdir)
		assert.Equal(t, "1.5", gotSpec.CPUs)
		assert.Equal(t, "512m", gotSpec.Memory)
	})

	t.Run("routes a relay entry through the relay instead of publishing it", func(t *testing.T) {
		var gotSpec cri.RunSpec
		useFakeRuntime(t, fakeRuntime{
			run: func(_ context.Context, spec cri.RunSpec) (string, error) {
				gotSpec = spec
				return "abc123", nil
			},
			inspect: func(context.Context, string) (cri.Container, error) {
				return cri.Container{Running: true}, nil
			},
		})

		result, err := Container{}.Up(t.Context(), &plugin.UpRequest{
			Step: "db",
			Env:  plugin.Env{RelaySOCKS5Addr: "127.0.0.1:54321"},
			Config: []byte(`{"image":"postgres:16","expose":{
				"postgres": {"port": 5432, "relay": true}
			}}`),
		}, &noopEmitter{})
		require.NoError(t, err)

		assert.Empty(t, gotSpec.Ports, "a relay entry must never appear in the docker --publish list")
		require.Len(t, result.ExposedPorts, 1)
		assert.Equal(t, plugin.ExposedPort{
			Name: "postgres", Protocol: "tcp", Relay: true, Upstream: "socks5://127.0.0.1:54321/db:5432",
		}, result.ExposedPorts[0])
	})

	t.Run("routes a relay+udp entry through the relay's udp pool", func(t *testing.T) {
		useFakeRuntime(t, fakeRuntime{
			run: func(context.Context, cri.RunSpec) (string, error) { return "abc123", nil },
			inspect: func(context.Context, string) (cri.Container, error) {
				return cri.Container{Running: true}, nil
			},
		})

		result, err := Container{}.Up(t.Context(), &plugin.UpRequest{
			Step: "db",
			Env: plugin.Env{
				RelaySOCKS5Addr:     "127.0.0.1:54321",
				RelaySOCKS5UDPAddrs: map[string]string{"40000": "127.0.0.1:41000"},
			},
			Config: []byte(`{"image":"coredns","expose":{
				"dns": {"port": 53, "protocol": "udp", "relay": true}
			}}`),
		}, &noopEmitter{})
		require.NoError(t, err)

		require.Len(t, result.ExposedPorts, 1)
		assert.Equal(t, plugin.ExposedPort{
			Name: "dns", Protocol: "udp", Relay: true,
			Upstream:      "socks5://127.0.0.1:54321/db:53",
			RelayUDPAddrs: map[string]string{"40000": "127.0.0.1:41000"},
		}, result.ExposedPorts[0])
	})

	t.Run("reports the container as its one Containers entry", func(t *testing.T) {
		useFakeRuntime(t, fakeRuntime{
			run: func(context.Context, cri.RunSpec) (string, error) { return "abc123", nil },
			inspect: func(context.Context, string) (cri.Container, error) {
				return cri.Container{Running: true, NetnsPath: "/proc/123/ns/net"}, nil
			},
		})

		result, err := Container{}.Up(t.Context(), &plugin.UpRequest{
			Step:   "web",
			Env:    plugin.Env{Project: "demo"},
			Config: []byte(`{"image":"nginx"}`),
		}, &noopEmitter{})
		require.NoError(t, err)
		require.Len(t, result.Containers, 1)
		assert.Equal(t, plugin.ContainerInfo{
			ID: "abc123", Name: containerName("demo", "web"), NetnsPath: "/proc/123/ns/net",
		}, result.Containers[0])
	})
}

func TestUpBuild(t *testing.T) {
	t.Run("builds the image, then runs the built tag", func(t *testing.T) {
		var calls []string
		var gotBuild cri.BuildSpec
		var gotRun cri.RunSpec
		useFakeRuntime(t, fakeRuntime{
			build: func(_ context.Context, spec cri.BuildSpec, out io.Writer) error {
				calls = append(calls, "build")
				gotBuild = spec
				_, _ = io.WriteString(out, "step 1/2\n")
				return nil
			},
			run: func(_ context.Context, spec cri.RunSpec) (string, error) {
				calls = append(calls, "run")
				gotRun = spec
				return "abc123", nil
			},
			inspect: func(context.Context, string) (cri.Container, error) {
				return cri.Container{Running: true}, nil
			},
		})
		emitter := &noopEmitter{}

		_, err := Container{}.Up(t.Context(), &plugin.UpRequest{
			Step: "api",
			Env:  plugin.Env{Project: "Demo", Scope: "env", ProjectDir: "/proj"},
			Config: []byte(`{"build":{
				"context": "./api",
				"dockerfile": "Dockerfile.dev",
				"args": {"GO_VERSION": "1.25"},
				"target": "dev"
			}}`),
		}, emitter)
		require.NoError(t, err)

		assert.Equal(t, []string{"build", "run"}, calls)
		assert.Equal(t, cri.BuildSpec{
			Context:    "/proj/api",
			Dockerfile: "/proj/api/Dockerfile.dev",
			Tag:        "kevin-demo-api:latest",
			Args:       map[string]string{"GO_VERSION": "1.25"},
			Target:     "dev",
			Labels:     gotRun.Labels,
		}, gotBuild)
		assert.Equal(t, "kevin-demo-api:latest", gotRun.Image)
		assert.Equal(t, "Demo:env:api", gotRun.Labels[cri.LabelURN])
		assert.Equal(t, "Demo", gotRun.Labels[cri.LabelProject])
	})

	t.Run("defaults the Dockerfile and keeps an absolute context", func(t *testing.T) {
		var gotBuild cri.BuildSpec
		useFakeRuntime(t, fakeRuntime{
			build: func(_ context.Context, spec cri.BuildSpec, _ io.Writer) error {
				gotBuild = spec
				return nil
			},
			run: func(context.Context, cri.RunSpec) (string, error) { return "abc123", nil },
			inspect: func(context.Context, string) (cri.Container, error) {
				return cri.Container{Running: true}, nil
			},
		})

		_, err := Container{}.Up(t.Context(), &plugin.UpRequest{
			Step:   "api",
			Env:    plugin.Env{Project: "demo", ProjectDir: "/proj"},
			Config: []byte(`{"build":{"context":"/abs/src"}}`),
		}, &noopEmitter{})
		require.NoError(t, err)

		assert.Equal(t, "/abs/src", gotBuild.Context)
		assert.Equal(t, "/abs/src/Dockerfile", gotBuild.Dockerfile)
	})

	t.Run("keeps an absolute Dockerfile", func(t *testing.T) {
		var gotBuild cri.BuildSpec
		useFakeRuntime(t, fakeRuntime{
			build: func(_ context.Context, spec cri.BuildSpec, _ io.Writer) error {
				gotBuild = spec
				return nil
			},
			run: func(context.Context, cri.RunSpec) (string, error) { return "abc123", nil },
			inspect: func(context.Context, string) (cri.Container, error) {
				return cri.Container{Running: true}, nil
			},
		})

		_, err := Container{}.Up(t.Context(), &plugin.UpRequest{
			Step:   "api",
			Env:    plugin.Env{Project: "demo", ProjectDir: "/proj"},
			Config: []byte(`{"build":{"context":".","dockerfile":"/etc/df"}}`),
		}, &noopEmitter{})
		require.NoError(t, err)

		assert.Equal(t, "/etc/df", gotBuild.Dockerfile)
	})

	t.Run("does not run a container when the build fails", func(t *testing.T) {
		ran := false
		useFakeRuntime(t, fakeRuntime{
			build: func(context.Context, cri.BuildSpec, io.Writer) error { return io.ErrUnexpectedEOF },
			run: func(context.Context, cri.RunSpec) (string, error) {
				ran = true
				return "abc123", nil
			},
		})

		_, err := Container{}.Up(t.Context(), &plugin.UpRequest{
			Step:   "api",
			Env:    plugin.Env{Project: "demo", ProjectDir: "/proj"},
			Config: []byte(`{"build":{"context":"."}}`),
		}, &noopEmitter{})
		require.ErrorIs(t, err, io.ErrUnexpectedEOF)
		assert.False(t, ran)
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
		assert.Equal(t, containerName("demo", "web"), removed)
	})

	t.Run("reports an unsupported engine before it touches any runtime", func(t *testing.T) {
		err := Container{}.Down(t.Context(), &plugin.DownRequest{
			Step: "web",
			Env:  plugin.Env{Engine: "bogus"},
		}, &noopEmitter{})
		require.ErrorIs(t, err, ErrUnsupportedEngine)
	})
}

func TestExportWithFakeEngine(t *testing.T) {
	t.Run("reports the container name and outputs", func(t *testing.T) {
		useFakeRuntime(t, fakeRuntime{
			inspect: func(context.Context, string) (cri.Container, error) {
				return cri.Container{ID: "abc123", Running: true, IPs: map[string]string{"net": "10.0.0.2"}}, nil
			},
		})

		result, err := Container{}.Export(t.Context(), &plugin.ExportRequest{
			Step: "web",
			Env:  plugin.Env{Project: "demo", Network: "net"},
		})
		require.NoError(t, err)
		assert.Equal(t, containerName("demo", "web"), result.Out["name"].Reveal())
		assert.Equal(t, "abc123", result.Out["id"].Reveal())
		assert.Equal(t, "10.0.0.2", result.Out["ip"].Reveal())
		require.Len(t, result.Containers, 1)
		assert.Equal(t, "abc123", result.Containers[0].ID)
		assert.Equal(t, containerName("demo", "web"), result.Containers[0].Name)
	})

	t.Run("fails when the container is not running", func(t *testing.T) {
		useFakeRuntime(t, fakeRuntime{
			inspect: func(context.Context, string) (cri.Container, error) {
				return cri.Container{Running: false}, nil
			},
		})

		_, err := Container{}.Export(t.Context(), &plugin.ExportRequest{Step: "web"})
		require.ErrorIs(t, err, ErrNotRunning)
	})

	t.Run("fails when the container was never created", func(t *testing.T) {
		useFakeRuntime(t, fakeRuntime{
			inspect: func(context.Context, string) (cri.Container, error) {
				return cri.Container{}, cri.ErrNotFound
			},
		})

		_, err := Container{}.Export(t.Context(), &plugin.ExportRequest{Step: "web"})
		require.ErrorIs(t, err, cri.ErrNotFound)
	})

	t.Run("reports an unsupported engine before it touches any runtime", func(t *testing.T) {
		_, err := Container{}.Export(t.Context(), &plugin.ExportRequest{
			Step: "web",
			Env:  plugin.Env{Engine: "bogus"},
		})
		require.ErrorIs(t, err, ErrUnsupportedEngine)
	})
}

// hostPortPattern matches an address on the loopback with any port.
const hostPortPattern = `^127\.0\.0\.1:[0-9]+$`
