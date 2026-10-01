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

	"github.com/justenwalker/kevin/internal/cri"
)

func TestMinikubeDriverTrustCA(t *testing.T) {
	const pem = "-----BEGIN CERTIFICATE-----\nabc\n-----END CERTIFICATE-----"
	nodes := []string{"demo", "demo-m02"}

	t.Run("passes when every node holds the certificate", func(t *testing.T) {
		var read []string
		d := &minikubeDriver{name: "demo", rt: fakeRuntime{exec: func(_ context.Context, container string, args ...string) (string, error) {
			read = append(read, container)
			assert.Equal(t, []string{"cat", "/etc/ssl/certs/kevin-root.pem"}, args)
			return pem + "\n", nil
		}}}

		require.NoError(t, d.TrustCA(t.Context(), nodes, pem, &capture{}))
		assert.Equal(t, nodes, read)
	})

	t.Run("a node without the certificate is an error", func(t *testing.T) {
		d := &minikubeDriver{name: "demo", rt: fakeRuntime{exec: func(context.Context, string, ...string) (string, error) {
			return "other", nil
		}}}

		require.ErrorIs(t, d.TrustCA(t.Context(), nodes, pem, &capture{}), ErrNotTrusted)
	})

	t.Run("an unreadable node is an error", func(t *testing.T) {
		d := &minikubeDriver{name: "demo", rt: fakeRuntime{exec: func(context.Context, string, ...string) (string, error) {
			return "", errors.New("exec failed")
		}}}

		require.Error(t, d.TrustCA(t.Context(), nodes, pem, &capture{}))
	})
}

func TestMinikubeDriverPointDNSAtRelay(t *testing.T) {
	t.Run("rewrites resolv.conf on every node", func(t *testing.T) {
		var got [][]string
		d := &minikubeDriver{name: "demo", rt: fakeRuntime{exec: func(_ context.Context, container string, args ...string) (string, error) {
			got = append(got, append([]string{container}, args...))
			return "", nil
		}}}

		require.NoError(t, d.PointDNSAtRelay(t.Context(), []string{"a", "b"}, "10.0.0.2"))
		assert.Equal(t, [][]string{
			{"a", "sh", "-c", "echo nameserver 10.0.0.2 > /etc/resolv.conf"},
			{"b", "sh", "-c", "echo nameserver 10.0.0.2 > /etc/resolv.conf"},
		}, got)
	})

	t.Run("a failing node is an error", func(t *testing.T) {
		d := &minikubeDriver{name: "demo", rt: fakeRuntime{exec: func(context.Context, string, ...string) (string, error) {
			return "", errors.New("exec failed")
		}}}

		require.Error(t, d.PointDNSAtRelay(t.Context(), []string{"a"}, "10.0.0.2"))
	})
}

func TestMinikubeDriverSetContainerdProxy(t *testing.T) {
	t.Run("writes the drop-in, then restarts containerd", func(t *testing.T) {
		var calls []string
		var unit string
		d := &minikubeDriver{name: "demo", rt: fakeRuntime{
			exec: func(_ context.Context, container string, args ...string) (string, error) {
				calls = append(calls, container+": "+args[0]+" "+args[1])
				return "", nil
			},
			execInput: func(_ context.Context, container string, stdin io.Reader, args ...string) (string, error) {
				b, err := io.ReadAll(stdin)
				unit = string(b)
				calls = append(calls, container+": "+args[0]+" "+args[1])
				return "", err
			},
		}}
		proxy := map[string]string{"NO_PROXY": "localhost", "HTTP_PROXY": "http://kevin:8080"}

		require.NoError(t, d.setContainerdProxy(t.Context(), "demo", proxy))

		assert.Equal(t, "[Service]\nEnvironment=\"HTTP_PROXY=http://kevin:8080\"\nEnvironment=\"NO_PROXY=localhost\"\n", unit)
		assert.Equal(t, []string{
			"demo: mkdir -p",
			"demo: tee /etc/systemd/system/containerd.service.d/http-proxy.conf",
			"demo: systemctl daemon-reload",
			"demo: systemctl restart",
			"demo: ctr version",
		}, calls)
	})

	t.Run("a failing restart is an error", func(t *testing.T) {
		d := &minikubeDriver{name: "demo", rt: fakeRuntime{exec: func(_ context.Context, _ string, args ...string) (string, error) {
			if args[0] == "systemctl" && args[1] == "restart" {
				return "", errors.New("restart failed")
			}
			return "", nil
		}}}

		require.Error(t, d.setContainerdProxy(t.Context(), "demo", map[string]string{"HTTP_PROXY": "x"}))
	})
}

func TestMinikubeDriverRefreshAccess(t *testing.T) {
	const kubeconfig = "clusters:\n- cluster:\n    certificate-authority-data: abc\n    server: https://127.0.0.1:58154\n  name: demo\n"

	setup := func(t *testing.T, ports map[string]string) *minikubeDriver {
		t.Helper()
		path := filepath.Join(t.TempDir(), "kubeconfig")
		require.NoError(t, os.WriteFile(path, []byte(kubeconfig), 0o600))
		return &minikubeDriver{name: "demo", kubeconfig: path, rt: fakeRuntime{
			inspect: func(_ context.Context, name string) (cri.Container, error) {
				assert.Equal(t, "demo", name)
				return cri.Container{Ports: ports}, nil
			},
		}}
	}

	t.Run("points the kubeconfig at the published port", func(t *testing.T) {
		d := setup(t, map[string]string{"8443/tcp": "127.0.0.1:58164", "22/tcp": "127.0.0.1:58161"})

		require.NoError(t, d.RefreshAccess(t.Context()))

		got, err := os.ReadFile(d.kubeconfig)
		require.NoError(t, err)
		assert.Equal(t, strings.Replace(kubeconfig, "58154", "58164", 1), string(got))
	})

	t.Run("a control plane with no API port is an error", func(t *testing.T) {
		d := setup(t, map[string]string{"22/tcp": "127.0.0.1:58161"})

		require.ErrorIs(t, d.RefreshAccess(t.Context()), ErrNoAPIPort)
	})

	t.Run("a missing kubeconfig is an error", func(t *testing.T) {
		d := setup(t, map[string]string{"8443/tcp": "127.0.0.1:58164"})
		d.kubeconfig = filepath.Join(t.TempDir(), "missing")

		require.Error(t, d.RefreshAccess(t.Context()))
	})

	t.Run("no runtime is an error", func(t *testing.T) {
		require.ErrorIs(t, (&minikubeDriver{}).RefreshAccess(t.Context()), ErrNoRuntime)
	})
}
