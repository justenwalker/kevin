package cosign

import (
	"context"
	"errors"
	"io"
	"os/exec"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"

	"github.com/justenwalker/kevin/internal/command/commandtest"
)

func TestVerifyBlobArgs(t *testing.T) {
	got := verifyBlobArgs("plugin.tar.gz", "plugin.tar.gz.sigstore.json", "ci@acme.example", "https://token.actions.githubusercontent.com")
	want := []string{
		"verify-blob",
		"--bundle", "plugin.tar.gz.sigstore.json",
		"--certificate-identity", "ci@acme.example",
		"--certificate-oidc-issuer", "https://token.actions.githubusercontent.com",
		"plugin.tar.gz",
	}
	assert.Equal(t, want, got)
}

func TestVerifyErr(t *testing.T) {
	t.Run("attaches cosign's stderr when present", func(t *testing.T) {
		cause := errors.New("exit status 1")
		got := verifyErr(cause, "Error: no matching signatures\n")
		require.ErrorIs(t, got, ErrVerifyFailed)
		require.ErrorIs(t, got, cause)
		assert.Equal(t, "cosign: cosign verify-blob failed: Error: no matching signatures", got.Error())
	})

	t.Run("falls back to the raw error with no stderr", func(t *testing.T) {
		underlying := errors.New("exit status 1")
		got := verifyErr(underlying, "  \n")
		require.ErrorIs(t, got, ErrVerifyFailed)
		require.ErrorIs(t, got, underlying)
	})
}

func TestClientVerifyBlob(t *testing.T) {
	t.Run("passes the bundle, identity, and issuer to cosign", func(t *testing.T) {
		runner := commandtest.NewMockRunner(t)
		runner.EXPECT().Run(mock.Anything, mock.Anything).RunAndReturn(
			func(_ context.Context, cmd *exec.Cmd) error {
				assert.Equal(t, verifyBlobArgs("/pkg", "/bundle", "me@example.com", "https://issuer")[0], cmd.Args[1])
				assert.Contains(t, cmd.Args, "me@example.com")
				assert.Contains(t, cmd.Args, "/bundle")
				return nil
			})

		require.NoError(t, New(runner).VerifyBlob(t.Context(), "/pkg", "/bundle", "me@example.com", "https://issuer"))
	})

	t.Run("reports cosign's stderr as ErrVerifyFailed", func(t *testing.T) {
		runner := commandtest.NewMockRunner(t)
		runner.EXPECT().Run(mock.Anything, mock.Anything).RunAndReturn(
			func(_ context.Context, cmd *exec.Cmd) error {
				_, _ = io.WriteString(cmd.Stderr, "no matching signatures\n")
				return errors.New("exit status 1")
			})

		err := New(runner).VerifyBlob(t.Context(), "/pkg", "/bundle", "id", "iss")
		require.ErrorIs(t, err, ErrVerifyFailed)
		assert.ErrorContains(t, err, "no matching signatures")
	})

	t.Run("reports ErrCosignNotFound when the runner cannot find cosign", func(t *testing.T) {
		runner := commandtest.NewMockRunner(t)
		runner.EXPECT().Run(mock.Anything, mock.Anything).Return(exec.ErrNotFound)

		err := New(runner).VerifyBlob(t.Context(), "/pkg", "/bundle", "id", "iss")
		require.ErrorIs(t, err, ErrCosignNotFound)
	})
}
