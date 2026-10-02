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

	minikubecmd "github.com/justenwalker/kevin/internal/command/minikube"
	"github.com/justenwalker/kevin/internal/cri"
	"github.com/justenwalker/kevin/plugin"
)

func TestNewMinikubeDriver(t *testing.T) {
	newDrv := func(cfg config) (*minikubeDriver, error) {
		return newMinikubeDriver(cfg, plugin.Env{}, "demo", "/kubeconfig", nil)
	}

	t.Run("workers with no settings are fine", func(t *testing.T) {
		_, err := newDrv(config{Workers: map[string]map[string]any{"worker": {}}})
		require.NoError(t, err)
	})

	t.Run("a worker with node settings is an error", func(t *testing.T) {
		_, err := newDrv(config{Workers: map[string]map[string]any{"worker": {"image": "x"}}})
		require.ErrorIs(t, err, ErrMinikubeWorkerSettings)
	})

	t.Run("one mount is fine", func(t *testing.T) {
		_, err := newDrv(config{Mounts: []mount{{Host: "/a", Container: "/a"}}})
		require.NoError(t, err)
	})

	t.Run("two mounts are an error", func(t *testing.T) {
		_, err := newDrv(config{Mounts: []mount{{Host: "/a", Container: "/a"}, {Host: "/b", Container: "/b"}}})
		require.ErrorIs(t, err, ErrMinikubeMounts)
	})
}

func TestMinikubeDriverStartSpec(t *testing.T) {
	t.Run("docker joins a network of its own", func(t *testing.T) {
		d := &minikubeDriver{
			name:       "demo",
			kubeconfig: "/ws/kubeconfig",
			env:        plugin.Env{Workspace: "/ws"},
			cfg: config{
				Workers: map[string]map[string]any{"a": {}, "b": {}},
				Minikube: minikubeConfig{
					KubernetesVersion: "v1.33.1", BaseImage: "kicbase:v1", Memory: "2g", CPUs: 2,
				},
			},
		}

		assert.Equal(t, minikubecmd.StartSpec{
			Name:              "demo",
			Driver:            "docker",
			Network:           "kevin-minikube-demo",
			Nodes:             3,
			Wait:              3,
			KubernetesVersion: "v1.33.1",
			BaseImage:         "kicbase:v1",
			Memory:            "2g",
			CPUs:              2,
			Home:              "/ws/minikube/demo/.minikube",
			Kubeconfig:        "/ws/kubeconfig",
		}, d.startSpec(createSpec{Wait: 3}))
	})

	t.Run("podman picks its own network", func(t *testing.T) {
		d := &minikubeDriver{name: "demo", env: plugin.Env{Engine: enginePodman}}

		got := d.startSpec(createSpec{})
		assert.Equal(t, "podman", got.Driver)
		assert.Empty(t, got.Network)
	})

	t.Run("the mount resolves a relative path and can be read-only", func(t *testing.T) {
		d := &minikubeDriver{name: "demo", env: plugin.Env{ProjectDir: "/proj"}, cfg: config{Mounts: []mount{{Host: "src", Container: "/workspace"}}}}
		assert.Equal(t, "/proj/src:/workspace", d.startSpec(createSpec{}).Mount)

		d.cfg.Mounts[0].ReadOnly = true
		assert.Equal(t, "/proj/src:/workspace:ro", d.startSpec(createSpec{}).Mount)
	})
}

func TestMinikubeDriverFingerprint(t *testing.T) {
	t.Run("built from the exact start arguments", func(t *testing.T) {
		d := &minikubeDriver{name: "demo"}
		want := strings.Join(minikubecmd.StartArgs(d.startSpec(createSpec{})), " ")

		got, err := d.Fingerprint(createSpec{})
		require.NoError(t, err)
		assert.Equal(t, want, got)
	})

	t.Run("the workspace and kubeconfig stay out", func(t *testing.T) {
		fingerprint := func(workspace, kubeconfig string) string {
			d := &minikubeDriver{name: "demo", kubeconfig: kubeconfig, env: plugin.Env{Workspace: workspace}}
			got, err := d.Fingerprint(createSpec{})
			require.NoError(t, err)
			return got
		}
		assert.Equal(t, fingerprint("/a", "/a/kubeconfig"), fingerprint("/b", "/b/kubeconfig"))
	})

	t.Run("a different proxy endpoint changes the fingerprint", func(t *testing.T) {
		fingerprint := func(proxy string) string {
			d := &minikubeDriver{cfg: config{Proxy: true}, name: "demo", env: plugin.Env{ProxyEnv: map[string]string{"HTTP_PROXY": proxy}}}
			got, err := d.Fingerprint(createSpec{})
			require.NoError(t, err)
			return got
		}
		assert.NotEqual(t, fingerprint("http://kevin:8080"), fingerprint("http://kevin:9090"))
	})

	t.Run("a different option changes the fingerprint", func(t *testing.T) {
		fingerprint := func(cpus int) string {
			d := &minikubeDriver{cfg: config{Minikube: minikubeConfig{CPUs: cpus}}, name: "demo"}
			got, err := d.Fingerprint(createSpec{})
			require.NoError(t, err)
			return got
		}
		assert.NotEqual(t, fingerprint(2), fingerprint(4))
	})

	t.Run("a different certificate changes the fingerprint", func(t *testing.T) {
		ca := filepath.Join(t.TempDir(), "ca.pem")
		d := &minikubeDriver{cfg: config{TrustCA: true}, name: "demo", env: plugin.Env{CAPath: ca}}

		require.NoError(t, os.WriteFile(ca, []byte("one"), 0o600))
		one, err := d.Fingerprint(createSpec{})
		require.NoError(t, err)

		require.NoError(t, os.WriteFile(ca, []byte("two"), 0o600))
		two, err := d.Fingerprint(createSpec{})
		require.NoError(t, err)

		assert.NotEqual(t, one, two)
	})

	t.Run("an unreadable certificate is an error", func(t *testing.T) {
		d := &minikubeDriver{cfg: config{TrustCA: true}, name: "demo", env: plugin.Env{CAPath: filepath.Join(t.TempDir(), "missing.pem")}}
		_, err := d.Fingerprint(createSpec{})
		require.Error(t, err)
	})
}

func TestMinikubeDriverNames(t *testing.T) {
	docker := &minikubeDriver{name: "demo"}
	assert.Equal(t, "demo", docker.Context())
	assert.Equal(t, "demo", docker.ControlPlane())
	assert.Equal(t, "kevin-minikube-demo", docker.network())

	podman := &minikubeDriver{name: "demo", env: plugin.Env{Engine: enginePodman}}
	assert.Equal(t, "demo", podman.network())
}

func TestMinikubeDriverNodes(t *testing.T) {
	list := func(names ...string) cri.Runtime {
		return fakeRuntime{listByLabel: func(_ context.Context, key, value string) ([]string, error) {
			assert.Equal(t, "created_by.minikube.sigs.k8s.io", key)
			assert.Equal(t, "true", value)
			return names, nil
		}}
	}

	t.Run("lists the control plane first and leaves out other profiles", func(t *testing.T) {
		d := &minikubeDriver{name: "demo", rt: list("demo-m03", "other", "demo-m02", "demo", "demo-x-m02", "demo2", "other-m02")}

		got, err := d.Nodes(t.Context())
		require.NoError(t, err)
		assert.Equal(t, []string{"demo", "demo-m02", "demo-m03"}, got)
	})

	t.Run("orders -m10 after -m09", func(t *testing.T) {
		d := &minikubeDriver{name: "demo", rt: list("demo-m10", "demo-m09", "demo", "demo-m02")}

		got, err := d.Nodes(t.Context())
		require.NoError(t, err)
		assert.Equal(t, []string{"demo", "demo-m02", "demo-m09", "demo-m10"}, got)
	})

	t.Run("a name with a pattern character matches only itself", func(t *testing.T) {
		d := &minikubeDriver{name: "a.c", rt: list("a.c", "abc", "abc-m02", "a.c-m02")}

		got, err := d.Nodes(t.Context())
		require.NoError(t, err)
		assert.Equal(t, []string{"a.c", "a.c-m02"}, got)
	})

	t.Run("no cluster is no nodes", func(t *testing.T) {
		d := &minikubeDriver{name: "demo", rt: list("other")}

		got, err := d.Nodes(t.Context())
		require.NoError(t, err)
		assert.Empty(t, got)
	})

	t.Run("a failing list is an error", func(t *testing.T) {
		d := &minikubeDriver{name: "demo", rt: fakeRuntime{listByLabel: func(context.Context, string, string) ([]string, error) {
			return nil, errors.New("list failed")
		}}}

		_, err := d.Nodes(t.Context())
		require.Error(t, err)
	})

	t.Run("no runtime is an error", func(t *testing.T) {
		_, err := (&minikubeDriver{name: "demo"}).Nodes(t.Context())
		require.ErrorIs(t, err, ErrNoRuntime)
	})
}

// stubbedMinikube builds a driver whose minikube calls only record
// themselves, with a workspace and a cache under t.TempDir.
func stubbedMinikube(t *testing.T, rt cri.Runtime) (*minikubeDriver, *[]string) {
	t.Helper()
	root := t.TempDir()
	var calls []string
	d := &minikubeDriver{
		name:       "demo",
		kubeconfig: filepath.Join(root, "kubeconfig"),
		env:        plugin.Env{Workspace: filepath.Join(root, "ws"), Project: "proj"},
		rt:         rt,
		cacheDir:   func() (string, error) { return filepath.Join(root, "cache"), nil },
	}
	d.deleteProfile = func(_ context.Context, name, home string, _ io.Writer) error {
		assert.Equal(t, d.home(), home)
		calls = append(calls, "delete "+name)
		return nil
	}
	d.start = func(_ context.Context, spec minikubecmd.StartSpec, _, _ io.Writer) error {
		assert.Equal(t, "demo", spec.Name)
		calls = append(calls, "start")
		return nil
	}
	return d, &calls
}

func TestMinikubeDriverCreate(t *testing.T) {
	nodesRuntime := func(exec func(context.Context, string, ...string) (string, error)) fakeRuntime {
		return fakeRuntime{
			exec:        exec,
			listByLabel: func(context.Context, string, string) ([]string, error) { return []string{"demo", "demo-m02"}, nil },
		}
	}

	t.Run("removes the old cluster, then starts a new one", func(t *testing.T) {
		d, calls := stubbedMinikube(t, nodesRuntime(nil))

		nodes, err := d.Create(t.Context(), createSpec{}, &capture{})
		require.NoError(t, err)
		assert.Equal(t, []string{"demo", "demo-m02"}, nodes)
		assert.Equal(t, []string{"delete demo", "delete demo", "delete demo-m02", "start"}, *calls,
			"the fake runtime reports the nodes before and after the start")
	})

	t.Run("docker gets an IPv4-only network before the cluster starts", func(t *testing.T) {
		var order []string
		rt := nodesRuntime(nil)
		rt.networkCreate = func(_ context.Context, name string, opts cri.NetworkOptions) error {
			order = append(order, "network "+name)
			assert.Equal(t, map[string]string{cri.LabelProject: "proj"}, opts.Labels)
			return nil
		}
		d, _ := stubbedMinikube(t, rt)
		d.start = func(context.Context, minikubecmd.StartSpec, io.Writer, io.Writer) error {
			order = append(order, "start")
			return nil
		}

		_, err := d.Create(t.Context(), createSpec{}, &capture{})
		require.NoError(t, err)
		assert.Equal(t, []string{"network kevin-minikube-demo", "start"}, order)
	})

	t.Run("podman creates no network", func(t *testing.T) {
		rt := nodesRuntime(nil)
		rt.networkCreate = func(context.Context, string, cri.NetworkOptions) error {
			t.Error("podman names its own network")
			return nil
		}
		d, _ := stubbedMinikube(t, rt)
		d.env.Engine = enginePodman

		_, err := d.Create(t.Context(), createSpec{}, &capture{})
		require.NoError(t, err)
	})

	t.Run("the proxy reaches containerd only when there is one", func(t *testing.T) {
		var dropIns int
		rt := nodesRuntime(nil)
		rt.execInput = func(_ context.Context, _ string, _ io.Reader, args ...string) (string, error) {
			if len(args) > 1 && args[1] == containerdProxyDropIn {
				dropIns++
			}
			return "", nil
		}

		d, _ := stubbedMinikube(t, rt)
		_, err := d.Create(t.Context(), createSpec{}, &capture{})
		require.NoError(t, err)
		assert.Zero(t, dropIns)

		d.cfg.Proxy = true
		d.env.ProxyEnv = map[string]string{"HTTP_PROXY": "http://kevin:8080"}
		_, err = d.Create(t.Context(), createSpec{}, &capture{})
		require.NoError(t, err)
		assert.Equal(t, 2, dropIns, "one drop-in per node")
	})

	t.Run("the certificate is written only with trust_ca", func(t *testing.T) {
		d, _ := stubbedMinikube(t, nodesRuntime(nil))
		d.env.CAPath = filepath.Join(t.TempDir(), "ca.pem")
		require.NoError(t, os.WriteFile(d.env.CAPath, []byte(testCAPEM), 0o600))
		cert := filepath.Join(d.home(), "certs", "kevin-root.pem")

		var seen bool
		d.start = func(context.Context, minikubecmd.StartSpec, io.Writer, io.Writer) error {
			_, err := os.Stat(cert)
			seen = err == nil
			return nil
		}

		_, err := d.Create(t.Context(), createSpec{}, &capture{})
		require.NoError(t, err)
		assert.False(t, seen)

		d.cfg.TrustCA = true
		_, err = d.Create(t.Context(), createSpec{}, &capture{})
		require.NoError(t, err)
		assert.True(t, seen, "the certificate is in place before minikube starts")
		got, err := os.ReadFile(cert)
		require.NoError(t, err)
		assert.Equal(t, testCAPEM, string(got))
	})

	t.Run("the cache is linked in the home", func(t *testing.T) {
		d, _ := stubbedMinikube(t, nodesRuntime(nil))
		var target string
		d.start = func(context.Context, minikubecmd.StartSpec, io.Writer, io.Writer) error {
			var err error
			target, err = os.Readlink(filepath.Join(d.home(), "cache"))
			return err
		}

		_, err := d.Create(t.Context(), createSpec{}, &capture{})
		require.NoError(t, err)
		cache, err := d.cacheDir()
		require.NoError(t, err)
		assert.Equal(t, cache, target)
	})

	t.Run("no runtime is an error", func(t *testing.T) {
		d, _ := stubbedMinikube(t, nil)
		_, err := d.Create(t.Context(), createSpec{}, &capture{})
		require.ErrorIs(t, err, ErrNoRuntime)
	})

	t.Run("a failure removes the cluster", func(t *testing.T) {
		d, calls := stubbedMinikube(t, fakeRuntime{})
		d.start = func(context.Context, minikubecmd.StartSpec, io.Writer, io.Writer) error {
			return errors.New("start failed")
		}

		_, err := d.Create(t.Context(), createSpec{}, &capture{})
		require.Error(t, err)
		assert.Equal(t, []string{"delete demo", "delete demo"}, *calls)
	})

	t.Run("retain keeps the cluster after a failure", func(t *testing.T) {
		d, calls := stubbedMinikube(t, fakeRuntime{})
		d.cfg.Retain = true
		d.start = func(context.Context, minikubecmd.StartSpec, io.Writer, io.Writer) error {
			return errors.New("start failed")
		}

		_, err := d.Create(t.Context(), createSpec{}, &capture{})
		require.Error(t, err)
		assert.Equal(t, []string{"delete demo"}, *calls)
	})

	t.Run("a cluster with no nodes is an error", func(t *testing.T) {
		d, _ := stubbedMinikube(t, fakeRuntime{})

		_, err := d.Create(t.Context(), createSpec{}, &capture{})
		require.ErrorIs(t, err, ErrNoNodes)
	})
}

func TestMinikubeDriverDelete(t *testing.T) {
	t.Run("deletes workers that outlive the cluster, and the network and state", func(t *testing.T) {
		var network string
		rt := fakeRuntime{
			listByLabel: func(context.Context, string, string) ([]string, error) {
				return []string{"demo-m02", "demo-m03", "other"}, nil
			},
			networkRemove: func(_ context.Context, name string) error {
				network = name
				return nil
			},
		}
		d, calls := stubbedMinikube(t, rt)
		cache, err := d.cacheDir()
		require.NoError(t, err)
		require.NoError(t, os.MkdirAll(cache, 0o755))
		require.NoError(t, os.WriteFile(filepath.Join(cache, "kept"), nil, 0o600))

		require.NoError(t, d.Delete(t.Context(), &capture{}))

		assert.Equal(t, []string{"delete demo", "delete demo-m02", "delete demo-m03"}, *calls)
		assert.Equal(t, "kevin-minikube-demo", network)
		assert.NoDirExists(t, filepath.Dir(d.home()))
		assert.FileExists(t, filepath.Join(cache, "kept"), "the shared cache is left alone")
	})

	t.Run("without a runtime it leaves the network", func(t *testing.T) {
		d, calls := stubbedMinikube(t, nil)

		require.NoError(t, d.Delete(t.Context(), &capture{}))
		assert.Equal(t, []string{"delete demo"}, *calls)
	})

	t.Run("a failing delete is an error", func(t *testing.T) {
		d, _ := stubbedMinikube(t, nil)
		d.deleteProfile = func(context.Context, string, string, io.Writer) error { return errors.New("delete failed") }

		require.Error(t, d.Delete(t.Context(), &capture{}))
	})
}

func TestMinikubeDriverKubectl(t *testing.T) {
	t.Run("runs the node's kubectl in the control plane", func(t *testing.T) {
		var gotNode string
		var gotArgs []string
		d := &minikubeDriver{name: "demo", rt: fakeRuntime{exec: func(_ context.Context, container string, args ...string) (string, error) {
			gotNode, gotArgs = container, args
			return "ok", nil
		}}}

		got, err := d.Kubectl(t.Context(), "get", "nodes")
		require.NoError(t, err)
		assert.Equal(t, "ok", got)
		assert.Equal(t, "demo", gotNode)
		assert.Equal(t, []string{
			"sh", "-c",
			`exec /var/lib/minikube/binaries/*/kubectl --kubeconfig /etc/kubernetes/admin.conf "$@"`,
			"kubectl", "get", "nodes",
		}, gotArgs)
	})

	t.Run("feeds stdin to kubectl", func(t *testing.T) {
		var gotStdin string
		d := &minikubeDriver{name: "demo", rt: fakeRuntime{execInput: func(_ context.Context, _ string, stdin io.Reader, _ ...string) (string, error) {
			b, err := io.ReadAll(stdin)
			gotStdin = string(b)
			return "", err
		}}}

		_, err := d.KubectlInput(t.Context(), strings.NewReader("manifest"), "apply", "-f", "-")
		require.NoError(t, err)
		assert.Equal(t, "manifest", gotStdin)
	})

	t.Run("no runtime is an error", func(t *testing.T) {
		d := &minikubeDriver{name: "demo"}
		_, err := d.Kubectl(t.Context(), "get", "nodes")
		require.ErrorIs(t, err, ErrNoRuntime)
		_, err = d.KubectlInput(t.Context(), strings.NewReader(""), "apply")
		require.ErrorIs(t, err, ErrNoRuntime)
	})
}

func TestMinikubeDriverLabelNodes(t *testing.T) {
	t.Run("labels the control plane and each worker by node name", func(t *testing.T) {
		var got [][]string
		d := &minikubeDriver{
			name: "demo",
			cfg:  config{Workers: map[string]map[string]any{"b": {}, "a": {}}},
			rt: fakeRuntime{exec: func(_ context.Context, container string, args ...string) (string, error) {
				assert.Equal(t, "demo", container)
				got = append(got, args[4:])
				return "", nil
			}},
		}

		require.NoError(t, d.LabelNodes(t.Context()))
		assert.Equal(t, [][]string{
			{"label", "node", "demo", "kevin.node=control-plane", "--overwrite"},
			{"label", "node", "demo-m02", "kevin.node=a", "--overwrite"},
			{"label", "node", "demo-m03", "kevin.node=b", "--overwrite"},
		}, got)
	})

	t.Run("a failing label is an error", func(t *testing.T) {
		d := &minikubeDriver{name: "demo", rt: fakeRuntime{exec: func(context.Context, string, ...string) (string, error) {
			return "", errors.New("exec failed")
		}}}

		require.Error(t, d.LabelNodes(t.Context()))
	})
}

func TestMinikubeDriverLoadImage(t *testing.T) {
	t.Run("loads the archive into the cluster", func(t *testing.T) {
		var name, home, path string
		d := &minikubeDriver{name: "demo", env: plugin.Env{Workspace: "/ws"}}
		d.loadImage = func(_ context.Context, n, h, p string, _ io.Writer) error {
			name, home, path = n, h, p
			return nil
		}

		require.NoError(t, d.LoadImage(t.Context(), "/tmp/relay.tar", &capture{}))
		assert.Equal(t, "demo", name)
		assert.Equal(t, "/ws/minikube/demo/.minikube", home)
		assert.Equal(t, "/tmp/relay.tar", path)
	})

	t.Run("a failing load is an error", func(t *testing.T) {
		d := &minikubeDriver{name: "demo"}
		d.loadImage = func(context.Context, string, string, string, io.Writer) error { return errors.New("load failed") }

		require.Error(t, d.LoadImage(t.Context(), "/tmp/relay.tar", &capture{}))
	})
}
