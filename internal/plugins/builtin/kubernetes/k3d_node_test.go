package kubernetes

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestK3dDriverTrustCA(t *testing.T) {
	const pem = "-----BEGIN CERTIFICATE-----\nabc\n-----END CERTIFICATE-----"
	nodes := []string{"k3d-demo-server-0", "k3d-demo-agent-0"}

	t.Run("passes when every node holds the certificate", func(t *testing.T) {
		var read []string
		d := &k3dDriver{name: "demo", rt: fakeRuntime{exec: func(_ context.Context, container string, args ...string) (string, error) {
			read = append(read, container)
			assert.Equal(t, []string{"cat", "/etc/ssl/certs/kevin-root.crt"}, args)
			return pem + "\n", nil
		}}}

		require.NoError(t, d.TrustCA(t.Context(), nodes, pem, &capture{}))
		assert.Equal(t, nodes, read)
	})

	t.Run("a node without the certificate is an error", func(t *testing.T) {
		d := &k3dDriver{name: "demo", rt: fakeRuntime{exec: func(context.Context, string, ...string) (string, error) {
			return "other", nil
		}}}

		require.ErrorIs(t, d.TrustCA(t.Context(), nodes, pem, &capture{}), ErrNotTrusted)
	})

	t.Run("an unreadable node is an error", func(t *testing.T) {
		d := &k3dDriver{name: "demo", rt: fakeRuntime{exec: func(context.Context, string, ...string) (string, error) {
			return "", errors.New("exec failed")
		}}}

		require.Error(t, d.TrustCA(t.Context(), nodes, pem, &capture{}))
	})
}

func TestK3dDriverPointDNSAtRelay(t *testing.T) {
	t.Run("rewrites resolv.conf on every node", func(t *testing.T) {
		var got [][]string
		d := &k3dDriver{name: "demo", rt: fakeRuntime{exec: func(_ context.Context, container string, args ...string) (string, error) {
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
		d := &k3dDriver{name: "demo", rt: fakeRuntime{exec: func(context.Context, string, ...string) (string, error) {
			return "", errors.New("exec failed")
		}}}

		require.Error(t, d.PointDNSAtRelay(t.Context(), []string{"a"}, "10.0.0.2"))
	})
}

func TestK3dDriverNoOps(t *testing.T) {
	d := &k3dDriver{}
	require.NoError(t, d.RefreshAccess(t.Context()))
	require.NoError(t, d.LabelNodes(t.Context()))
}
