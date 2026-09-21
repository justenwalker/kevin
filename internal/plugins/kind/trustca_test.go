package kind

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"slices"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const testCAPEM = "-----BEGIN CERTIFICATE-----\nMIIB...\n-----END CERTIFICATE-----\n"

func TestNormalizePEM(t *testing.T) {
	assert.Equal(t, "line one\nline two", normalizePEM("  line one\r\nline two  "))
}

func TestVerifyTrusted(t *testing.T) {
	t.Run("the bundle holds the certificate", func(t *testing.T) {
		rt := fakeRuntime{exec: func(context.Context, string, ...string) (string, error) {
			return "other stuff\n" + testCAPEM, nil
		}}
		require.NoError(t, verifyTrusted(t.Context(), rt, "demo-control-plane", testCAPEM))
	})

	t.Run("a CRLF bundle still matches an LF certificate", func(t *testing.T) {
		rt := fakeRuntime{exec: func(context.Context, string, ...string) (string, error) {
			return "-----BEGIN CERTIFICATE-----\r\nMIIB...\r\n-----END CERTIFICATE-----\r\n", nil
		}}
		require.NoError(t, verifyTrusted(t.Context(), rt, "demo-control-plane", testCAPEM))
	})

	t.Run("reading the bundle fails", func(t *testing.T) {
		rt := fakeRuntime{exec: func(context.Context, string, ...string) (string, error) {
			return "", errors.New("exec: no such container")
		}}
		err := verifyTrusted(t.Context(), rt, "demo-control-plane", testCAPEM)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "read the trust store")
	})

	t.Run("the bundle does not hold the certificate", func(t *testing.T) {
		rt := fakeRuntime{exec: func(context.Context, string, ...string) (string, error) {
			return "unrelated bundle contents\n", nil
		}}
		err := verifyTrusted(t.Context(), rt, "demo-control-plane", testCAPEM)
		require.ErrorIs(t, err, ErrNotTrusted)
	})
}

func TestWaitContainerdReady(t *testing.T) {
	t.Run("ready on the first check", func(t *testing.T) {
		rt := fakeRuntime{exec: func(context.Context, string, ...string) (string, error) {
			return "ctr github.com/containerd/containerd 1.7.0", nil
		}}
		require.NoError(t, waitContainerdReady(t.Context(), rt, "demo-control-plane"))
	})

	t.Run("a canceled context stops the poll instead of waiting out the timeout", func(t *testing.T) {
		ctx, cancel := context.WithCancel(t.Context())
		cancel()

		rt := fakeRuntime{exec: func(context.Context, string, ...string) (string, error) {
			return "", errors.New("containerd not ready")
		}}
		err := waitContainerdReady(ctx, rt, "demo-control-plane")
		require.ErrorIs(t, err, context.Canceled)
	})
}

// installTrustCARuntime builds a fakeRuntime that walks installTrustCAOnNode's
// call sequence for one node (write the cert, refresh the trust store,
// verify it, restart containerd, wait for it) and fails on the callN'th
// Exec/ExecInput call, or never when callN is 0. update-ca-certificates'
// own result is ignored by the caller, so failing that call must not surface.
func installTrustCARuntime(callN int) fakeRuntime {
	call := 0
	fails := func() bool {
		call++
		return call == callN
	}
	return fakeRuntime{
		execInput: func(context.Context, string, io.Reader, ...string) (string, error) {
			if fails() {
				return "", errors.New("exec failed")
			}
			return "", nil
		},
		exec: func(_ context.Context, _ string, args ...string) (string, error) {
			ignoreFailure := slices.Contains(args, "update-ca-certificates")
			failed := fails()
			switch {
			case failed && ignoreFailure:
				return "", errors.New("update-ca-certificates warned")
			case failed:
				return "", errors.New("exec failed")
			case slices.Contains(args, "cat"):
				return testCAPEM, nil
			default:
				return "", nil
			}
		},
	}
}

func TestInstallTrustCAOnNode(t *testing.T) {
	t.Run("happy path writes, verifies, and restarts containerd", func(t *testing.T) {
		require.NoError(t, installTrustCAOnNode(t.Context(), installTrustCARuntime(0), "demo-control-plane", testCAPEM))
	})

	t.Run("writing the certificate fails", func(t *testing.T) {
		err := installTrustCAOnNode(t.Context(), installTrustCARuntime(1), "demo-control-plane", testCAPEM)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "write the kevin root certificate into")
	})

	t.Run("update-ca-certificates warning is ignored", func(t *testing.T) {
		require.NoError(t, installTrustCAOnNode(t.Context(), installTrustCARuntime(2), "demo-control-plane", testCAPEM))
	})

	t.Run("verifying the trust store fails", func(t *testing.T) {
		err := installTrustCAOnNode(t.Context(), installTrustCARuntime(3), "demo-control-plane", testCAPEM)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "read the trust store")
	})

	t.Run("restarting containerd fails", func(t *testing.T) {
		err := installTrustCAOnNode(t.Context(), installTrustCARuntime(4), "demo-control-plane", testCAPEM)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "restart containerd on")
	})
}

func TestInstallTrustCA(t *testing.T) {
	t.Run("installs on every node", func(t *testing.T) {
		var nodes []string
		rt := fakeRuntime{
			execInput: func(_ context.Context, container string, _ io.Reader, _ ...string) (string, error) {
				nodes = append(nodes, container)
				return "", nil
			},
			exec: func(_ context.Context, _ string, args ...string) (string, error) {
				if slices.Contains(args, "cat") {
					return testCAPEM, nil
				}
				return "", nil
			},
		}

		out := &capture{}
		err := installTrustCA(t.Context(), rt, []string{"demo-control-plane", "demo-worker"}, testCAPEM, out)
		require.NoError(t, err)
		assert.Equal(t, []string{"demo-control-plane", "demo-worker"}, nodes)
	})

	t.Run("a node failing stops before the next one", func(t *testing.T) {
		var nodes []string
		rt := fakeRuntime{
			execInput: func(_ context.Context, container string, _ io.Reader, _ ...string) (string, error) {
				nodes = append(nodes, container)
				if container == "demo-worker" {
					return "", errors.New("exec failed")
				}
				return "", nil
			},
			exec: func(_ context.Context, _ string, args ...string) (string, error) {
				if slices.Contains(args, "cat") {
					return testCAPEM, nil
				}
				return "", nil
			},
		}

		err := installTrustCA(t.Context(), rt, []string{"demo-worker", "demo-control-plane"}, testCAPEM, &capture{})
		require.Error(t, err)
		assert.Equal(t, []string{"demo-worker"}, nodes, "a node after the failure is never reached")
	})
}

func TestTrustCAFromPath(t *testing.T) {
	rt := fakeRuntime{
		execInput: func(context.Context, string, io.Reader, ...string) (string, error) { return "", nil },
		exec: func(_ context.Context, _ string, args ...string) (string, error) {
			if slices.Contains(args, "cat") {
				return testCAPEM, nil
			}
			return "", nil
		},
	}

	t.Run("reads the certificate from the given path", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "ca.pem")
		require.NoError(t, os.WriteFile(path, []byte(testCAPEM), 0o600))

		require.NoError(t, trustCAFromPath(t.Context(), rt, []string{"demo-control-plane"}, path, &capture{}))
	})

	t.Run("a missing path is an error", func(t *testing.T) {
		err := trustCAFromPath(t.Context(), rt, []string{"demo-control-plane"}, filepath.Join(t.TempDir(), "missing.pem"), &capture{})
		require.Error(t, err)
		assert.Contains(t, err.Error(), "read the kevin root certificate")
	})
}
