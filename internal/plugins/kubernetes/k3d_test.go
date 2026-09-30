package kubernetes

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/justenwalker/kevin/internal/clusterrelay"
	"github.com/justenwalker/kevin/internal/cri"
	"github.com/justenwalker/kevin/internal/k3dcmd"
	"github.com/justenwalker/kevin/plugin"
)

func TestNewK3dDriver(t *testing.T) {
	newDrv := func(cfg config) (*k3dDriver, error) {
		return newK3dDriver(cfg, plugin.Env{}, "demo-cluster", "/kubeconfig", nil)
	}

	t.Run("workers with relay is fine", func(t *testing.T) {
		_, err := newDrv(config{Workers: map[string]map[string]any{"worker": {}}, Relay: true})
		require.NoError(t, err)
	})

	t.Run("a worker with node settings is an error", func(t *testing.T) {
		_, err := newDrv(config{Workers: map[string]map[string]any{"worker": {"image": "x"}}})
		require.ErrorIs(t, err, ErrK3dWorkerSettings)
	})
}

func TestK3dDriverCreateSpec(t *testing.T) {
	cfg := config{
		Proxy:   true,
		TrustCA: true,
		Workers: map[string]map[string]any{"b": {}, "a": {}},
		K3d:     k3dConfig{Image: "rancher/k3s:v1.34.1-k3s1"},
	}
	env := plugin.Env{
		CAPath:   "/ws/ca.pem",
		ProxyEnv: map[string]string{"HTTP_PROXY": "http://kevin:8080"},
	}
	d := &k3dDriver{cfg: cfg, env: env, name: "demo"}

	got := d.createSpec(createSpec{Ports: clusterrelay.Ports{TCP: 54321}, Wait: 3})

	assert.Equal(t, k3dcmd.CreateSpec{
		Name:    "demo",
		Network: "kevin-k3d-demo",
		Image:   "rancher/k3s:v1.34.1-k3s1",
		Agents:  2,
		Wait:    3,
		Env:     map[string]string{"HTTP_PROXY": "http://kevin:8080"},
		Ports:   []string{"127.0.0.1:54321:1080/tcp@server:0"},
		Volumes: []string{"/ws/ca.pem:/etc/ssl/certs/kevin-root.crt@server:*;agent:*"},
		NodeLabels: []string{
			"kevin.node=control-plane@server:0",
			"kevin.node=a@agent:0",
			"kevin.node=b@agent:1",
		},
		K3sArgs: []string{
			"--cluster-cidr=10.42.0.0/16@server:*",
			"--service-cidr=10.43.0.0/16@server:*",
		},
	}, got)

	t.Run("retain keeps a failed cluster", func(t *testing.T) {
		d.cfg.Retain = true
		assert.True(t, d.createSpec(createSpec{}).NoRollback)
	})

	t.Run("proxy: false sets no env", func(t *testing.T) {
		d.cfg.Proxy = false
		assert.Nil(t, d.createSpec(createSpec{}).Env)
	})

	t.Run("trust_ca: false mounts no certificate", func(t *testing.T) {
		d.cfg.TrustCA = false
		assert.Nil(t, d.createSpec(createSpec{}).Volumes)
	})
}

func TestK3dPortFlags(t *testing.T) {
	t.Run("no relay wanted adds no flags", func(t *testing.T) {
		assert.Nil(t, k3dPortFlags(clusterrelay.Ports{}))
	})

	t.Run("tcp plus a udp pool", func(t *testing.T) {
		got := k3dPortFlags(clusterrelay.Ports{TCP: 54321, UDP: []int{41000, 41001}})
		assert.Equal(t, []string{
			"127.0.0.1:54321:1080/tcp@server:0",
			"127.0.0.1:41000:40000/udp@server:0",
			"127.0.0.1:41001:40001/udp@server:0",
		}, got)
	})
}

func TestK3dDriverFingerprint(t *testing.T) {
	t.Run("built from the exact create arguments", func(t *testing.T) {
		d := &k3dDriver{cfg: config{}, name: "demo"}
		want := strings.Join(k3dcmd.CreateArgs(d.createSpec(createSpec{})), " ")

		got, err := d.Fingerprint(createSpec{})
		require.NoError(t, err)
		assert.Equal(t, want, got)
	})

	t.Run("a different proxy endpoint changes the fingerprint", func(t *testing.T) {
		fingerprint := func(proxy string) string {
			d := &k3dDriver{cfg: config{Proxy: true}, env: plugin.Env{ProxyEnv: map[string]string{"HTTP_PROXY": proxy}}, name: "demo"}
			got, err := d.Fingerprint(createSpec{})
			require.NoError(t, err)
			return got
		}
		assert.NotEqual(t, fingerprint("http://host:1"), fingerprint("http://host:2"))
	})

	t.Run("a different certificate changes the fingerprint", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "ca.pem")
		d := &k3dDriver{cfg: config{TrustCA: true}, env: plugin.Env{CAPath: path}, name: "demo"}

		require.NoError(t, os.WriteFile(path, []byte("one"), 0o600))
		a, err := d.Fingerprint(createSpec{})
		require.NoError(t, err)
		require.NoError(t, os.WriteFile(path, []byte("two"), 0o600))
		b, err := d.Fingerprint(createSpec{})
		require.NoError(t, err)

		assert.NotEqual(t, a, b, "the nodes read the certificate once, at start")
	})

	t.Run("an unreadable certificate is an error", func(t *testing.T) {
		d := &k3dDriver{cfg: config{TrustCA: true}, env: plugin.Env{CAPath: "/nonexistent/ca.pem"}, name: "demo"}
		_, err := d.Fingerprint(createSpec{})
		require.Error(t, err)
	})
}

func TestK3dDriverDockerEnv(t *testing.T) {
	t.Run("docker needs no variables", func(t *testing.T) {
		d := &k3dDriver{env: plugin.Env{Engine: "docker"}}
		got, err := d.dockerEnv(t.Context())
		require.NoError(t, err)
		assert.Empty(t, got)
	})

	t.Run("podman points DOCKER_HOST at the podman socket", func(t *testing.T) {
		d := &k3dDriver{env: plugin.Env{Engine: "podman"}}
		d.socket = func(context.Context) (string, error) { return "/run/podman/podman.sock", nil }

		got, err := d.dockerEnv(t.Context())
		require.NoError(t, err)
		assert.Equal(t, map[string]string{"DOCKER_HOST": "unix:///run/podman/podman.sock"}, got)
	})

	t.Run("a missing socket is an error", func(t *testing.T) {
		d := &k3dDriver{env: plugin.Env{Engine: "podman"}}
		d.socket = func(context.Context) (string, error) { return "", errors.New("no socket") }

		_, err := d.dockerEnv(t.Context())
		require.Error(t, err)
	})

	t.Run("every k3d call gets the variables", func(t *testing.T) {
		want := map[string]string{"DOCKER_HOST": "unix:///run/podman/podman.sock"}
		d := &k3dDriver{env: plugin.Env{Engine: "podman"}, name: "demo", kubeconfig: "/kubeconfig"}
		d.socket = func(context.Context) (string, error) { return "/run/podman/podman.sock", nil }
		d.freePort = func(context.Context) (int, error) { return 6550, nil }
		d.create = func(_ context.Context, spec k3dcmd.CreateSpec, _, _ io.Writer) error {
			assert.Equal(t, want, spec.CommandEnv)
			return nil
		}
		d.writeKubeconfig = func(_ context.Context, _, _ string, env map[string]string) error {
			assert.Equal(t, want, env)
			return nil
		}
		d.listNodes = func(_ context.Context, _ string, env map[string]string) ([]string, error) {
			assert.Equal(t, want, env)
			return []string{"k3d-demo-server-0"}, nil
		}
		d.deleteCluster = func(_ context.Context, _ string, env map[string]string, _ io.Writer) error {
			assert.Equal(t, want, env)
			return nil
		}
		d.importImage = func(_ context.Context, spec k3dcmd.ImageImportSpec, _ io.Writer) error {
			assert.Equal(t, want, spec.CommandEnv)
			return nil
		}

		_, err := d.createNodes(t.Context(), createSpec{}, &capture{})
		require.NoError(t, err)
		require.NoError(t, d.Delete(t.Context(), &capture{}))
		require.NoError(t, d.LoadImage(t.Context(), "/tmp/relay.tar", &capture{}))
	})
}

func TestK3dDriverNames(t *testing.T) {
	d := &k3dDriver{name: "demo"}

	assert.Equal(t, "k3d-demo", d.Context())
	assert.Equal(t, "k3d-demo-server-0", d.ControlPlane())
	assert.Equal(t, []string{"10.42.0.0/16", "10.43.0.0/16"}, d.ClusterCIDRs())
}

func TestK3dDriverCreate(t *testing.T) {
	stubbed := func(rt cri.Runtime) (*k3dDriver, *[]string) {
		var calls []string
		d := &k3dDriver{name: "demo", kubeconfig: "/kubeconfig", rt: rt}
		d.deleteCluster = func(context.Context, string, map[string]string, io.Writer) error {
			calls = append(calls, "delete")
			return nil
		}
		d.freePort = func(context.Context) (int, error) { return 6550, nil }
		d.create = func(_ context.Context, spec k3dcmd.CreateSpec, _, _ io.Writer) error {
			assert.Equal(t, 6550, spec.APIPort)
			calls = append(calls, "create")
			return nil
		}
		d.writeKubeconfig = func(_ context.Context, name, path string, _ map[string]string) error {
			assert.Equal(t, "demo", name)
			assert.Equal(t, "/kubeconfig", path)
			calls = append(calls, "kubeconfig")
			return nil
		}
		d.listNodes = func(context.Context, string, map[string]string) ([]string, error) {
			return []string{"k3d-demo-server-0"}, nil
		}
		return d, &calls
	}

	t.Run("removes the old cluster, creates a new one, and writes the kubeconfig", func(t *testing.T) {
		d, calls := stubbed(fakeRuntime{})

		nodes, err := d.Create(t.Context(), createSpec{}, &capture{})
		require.NoError(t, err)
		assert.Equal(t, []string{"k3d-demo-server-0"}, nodes)
		assert.Equal(t, []string{"delete", "create", "kubeconfig"}, *calls)
	})

	t.Run("creates the network before the cluster", func(t *testing.T) {
		var network string
		d, _ := stubbed(fakeRuntime{networkCreate: func(_ context.Context, name string, _ cri.NetworkOptions) error {
			network = name
			return nil
		}})

		_, err := d.Create(t.Context(), createSpec{}, &capture{})
		require.NoError(t, err)
		assert.Equal(t, "kevin-k3d-demo", network)
	})

	t.Run("no runtime is an error", func(t *testing.T) {
		d, _ := stubbed(nil)
		_, err := d.Create(t.Context(), createSpec{}, &capture{})
		require.ErrorIs(t, err, ErrNoRuntime)
	})

	t.Run("a failure removes the cluster", func(t *testing.T) {
		d, calls := stubbed(fakeRuntime{})
		d.create = func(context.Context, k3dcmd.CreateSpec, io.Writer, io.Writer) error {
			return errors.New("create failed")
		}

		_, err := d.Create(t.Context(), createSpec{}, &capture{})
		require.Error(t, err)
		assert.Equal(t, []string{"delete", "delete"}, *calls)
	})

	t.Run("retain keeps the cluster after a failure", func(t *testing.T) {
		d, calls := stubbed(fakeRuntime{})
		d.cfg.Retain = true
		d.create = func(context.Context, k3dcmd.CreateSpec, io.Writer, io.Writer) error {
			return errors.New("create failed")
		}

		_, err := d.Create(t.Context(), createSpec{}, &capture{})
		require.Error(t, err)
		assert.Equal(t, []string{"delete"}, *calls)
	})

	t.Run("a cluster with no nodes is an error", func(t *testing.T) {
		d, _ := stubbed(fakeRuntime{})
		d.listNodes = func(context.Context, string, map[string]string) ([]string, error) { return nil, nil }

		_, err := d.Create(t.Context(), createSpec{}, &capture{})
		require.ErrorIs(t, err, ErrNoNodes)
	})
}

func TestK3dDriverDelete(t *testing.T) {
	t.Run("removes the cluster and its network", func(t *testing.T) {
		var network string
		rt := fakeRuntime{networkRemove: func(_ context.Context, name string) error {
			network = name
			return nil
		}}
		d := &k3dDriver{name: "demo", rt: rt}
		d.deleteCluster = func(context.Context, string, map[string]string, io.Writer) error { return nil }

		require.NoError(t, d.Delete(t.Context(), &capture{}))
		assert.Equal(t, "kevin-k3d-demo", network)
	})

	t.Run("without a runtime it leaves the network", func(t *testing.T) {
		d := &k3dDriver{name: "demo"}
		d.deleteCluster = func(context.Context, string, map[string]string, io.Writer) error { return nil }

		require.NoError(t, d.Delete(t.Context(), &capture{}))
	})

	t.Run("a failing delete is an error", func(t *testing.T) {
		d := &k3dDriver{name: "demo"}
		d.deleteCluster = func(context.Context, string, map[string]string, io.Writer) error { return errors.New("delete failed") }

		require.Error(t, d.Delete(t.Context(), &capture{}))
	})
}

func TestK3dDriverKubectl(t *testing.T) {
	t.Run("runs kubectl in the server node with the k3s kubeconfig", func(t *testing.T) {
		var gotNode string
		var gotArgs []string
		d := &k3dDriver{name: "demo", rt: fakeRuntime{exec: func(_ context.Context, container string, args ...string) (string, error) {
			gotNode, gotArgs = container, args
			return "ok", nil
		}}}

		got, err := d.Kubectl(t.Context(), "get", "nodes")
		require.NoError(t, err)
		assert.Equal(t, "ok", got)
		assert.Equal(t, "k3d-demo-server-0", gotNode)
		assert.Equal(t, []string{"kubectl", "--kubeconfig", "/etc/rancher/k3s/k3s.yaml", "get", "nodes"}, gotArgs)
	})

	t.Run("feeds stdin to kubectl", func(t *testing.T) {
		var gotStdin string
		d := &k3dDriver{name: "demo", rt: fakeRuntime{execInput: func(_ context.Context, _ string, stdin io.Reader, _ ...string) (string, error) {
			b, err := io.ReadAll(stdin)
			gotStdin = string(b)
			return "", err
		}}}

		_, err := d.KubectlInput(t.Context(), strings.NewReader("manifest"), "apply", "-f", "-")
		require.NoError(t, err)
		assert.Equal(t, "manifest", gotStdin)
	})

	t.Run("no runtime is an error", func(t *testing.T) {
		d := &k3dDriver{name: "demo"}
		_, err := d.Kubectl(t.Context(), "get", "nodes")
		require.ErrorIs(t, err, ErrNoRuntime)
		_, err = d.KubectlInput(t.Context(), strings.NewReader(""), "apply")
		require.ErrorIs(t, err, ErrNoRuntime)
	})
}

func TestK3dDriverLoadImage(t *testing.T) {
	t.Run("imports the archive into the cluster", func(t *testing.T) {
		var got k3dcmd.ImageImportSpec
		d := &k3dDriver{name: "demo"}
		d.importImage = func(_ context.Context, spec k3dcmd.ImageImportSpec, _ io.Writer) error {
			got = spec
			return nil
		}

		require.NoError(t, d.LoadImage(t.Context(), "/tmp/relay.tar", &capture{}))
		assert.Equal(t, k3dcmd.ImageImportSpec{Name: "demo", Path: "/tmp/relay.tar", CommandEnv: map[string]string{}}, got)
	})

	t.Run("a failing import is an error", func(t *testing.T) {
		d := &k3dDriver{name: "demo"}
		d.importImage = func(context.Context, k3dcmd.ImageImportSpec, io.Writer) error { return errors.New("import failed") }

		require.Error(t, d.LoadImage(t.Context(), "/tmp/relay.tar", &capture{}))
	})
}

func TestK3dDriverNodes(t *testing.T) {
	t.Run("a failing list is an error", func(t *testing.T) {
		d := &k3dDriver{name: "demo"}
		d.listNodes = func(context.Context, string, map[string]string) ([]string, error) {
			return nil, errors.New("list failed")
		}

		_, err := d.Nodes(t.Context())
		require.Error(t, err)
	})
}

func requireK3d(t *testing.T) {
	t.Helper()
	if err := k3dcmd.Available(t.Context()); err != nil {
		t.Skip("k3d is unavailable:", err)
	}
}

func TestFreeLoopbackPort(t *testing.T) {
	port, err := freeLoopbackPort(t.Context())
	require.NoError(t, err)
	assert.Positive(t, port)
}
