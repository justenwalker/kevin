package kubernetes

import (
	"context"
	"errors"
	"io"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestNodeExec(t *testing.T) {
	argv := func(args []string) []string { return append([]string{"kubectl"}, args...) }

	t.Run("Kubectl runs in the control plane through the driver's argv", func(t *testing.T) {
		var gotNode string
		var gotArgs []string
		n := nodeExec{
			driver: "x", controlPlane: "cp", kubectlArgv: argv,
			rt: fakeRuntime{exec: func(_ context.Context, c string, a ...string) (string, error) {
				gotNode, gotArgs = c, a
				return "out", nil
			}},
		}

		out, err := n.Kubectl(t.Context(), "get", "nodes")

		require.NoError(t, err)
		assert.Equal(t, "out", out)
		assert.Equal(t, "cp", gotNode)
		assert.Equal(t, []string{"kubectl", "get", "nodes"}, gotArgs)
	})

	t.Run("Kubectl without a runtime reports ErrNoRuntime", func(t *testing.T) {
		_, err := nodeExec{driver: "x", kubectlArgv: argv}.Kubectl(t.Context(), "get")

		require.ErrorIs(t, err, ErrNoRuntime)
	})

	t.Run("PointDNSAtRelay writes resolv.conf on every node", func(t *testing.T) {
		var nodes, cmds []string
		n := nodeExec{
			driver: "x",
			rt: fakeRuntime{exec: func(_ context.Context, c string, a ...string) (string, error) {
				nodes, cmds = append(nodes, c), append(cmds, strings.Join(a, " "))
				return "", nil
			}},
		}

		require.NoError(t, n.PointDNSAtRelay(t.Context(), []string{"a", "b"}, "10.0.0.2"))

		assert.Equal(t, []string{"a", "b"}, nodes)
		assert.Equal(t, "sh -c echo nameserver 10.0.0.2 > /etc/resolv.conf", cmds[0])
	})

	t.Run("PointDNSAtRelay names the node that failed", func(t *testing.T) {
		n := nodeExec{
			driver: "x",
			rt: fakeRuntime{exec: func(context.Context, string, ...string) (string, error) {
				return "", errors.New("boom")
			}},
		}

		err := n.PointDNSAtRelay(t.Context(), []string{"a"}, "10.0.0.2")

		require.ErrorContains(t, err, "point a's dns")
	})

	t.Run("verifyFileHolds accepts a file that holds the certificate", func(t *testing.T) {
		n := nodeExec{
			driver: "x",
			rt: fakeRuntime{exec: func(context.Context, string, ...string) (string, error) {
				return "-----BEGIN CERTIFICATE-----\nabc\n-----END CERTIFICATE-----\n", nil
			}},
		}

		err := n.verifyFileHolds(t.Context(), []string{"a"}, "/ca", "-----BEGIN CERTIFICATE-----\nabc\n-----END CERTIFICATE-----\n")

		require.NoError(t, err)
	})

	t.Run("verifyFileHolds reports ErrNotTrusted for a different file", func(t *testing.T) {
		n := nodeExec{
			driver: "x",
			rt: fakeRuntime{exec: func(context.Context, string, ...string) (string, error) {
				return "other", nil
			}},
		}

		err := n.verifyFileHolds(t.Context(), []string{"a"}, "/ca", "-----BEGIN CERTIFICATE-----\nabc\n-----END CERTIFICATE-----\n")

		require.ErrorIs(t, err, ErrNotTrusted)
	})

	t.Run("KubectlInput feeds stdin and labels errors with the driver", func(t *testing.T) {
		var got string
		n := nodeExec{
			driver: "x", controlPlane: "cp", kubectlArgv: argv,
			rt: fakeRuntime{execInput: func(_ context.Context, _ string, in io.Reader, _ ...string) (string, error) {
				b, _ := io.ReadAll(in)
				got = string(b)
				return "", errors.New("boom")
			}},
		}

		_, err := n.KubectlInput(t.Context(), strings.NewReader("manifest"), "apply", "-f", "-")

		assert.Equal(t, "manifest", got)
		require.ErrorContains(t, err, "kubernetes: x: kubectl")
	})
}
