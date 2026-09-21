package sigstorepkg

import (
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestLookPath(t *testing.T) {
	t.Run("reports ErrCosignNotFound when PATH has no cosign", func(t *testing.T) {
		t.Setenv("PATH", t.TempDir())
		_, err := LookPath()
		require.ErrorIs(t, err, ErrCosignNotFound)
	})
}

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
		got := verifyErr(errors.New("exit status 1"), "Error: no matching signatures\n")
		require.ErrorIs(t, got, ErrVerifyFailed)
		assert.Equal(t, "sigstorepkg: cosign verify-blob failed: Error: no matching signatures", got.Error())
	})

	t.Run("falls back to the raw error with no stderr", func(t *testing.T) {
		underlying := errors.New("exit status 1")
		got := verifyErr(underlying, "  \n")
		require.ErrorIs(t, got, ErrVerifyFailed)
		require.ErrorIs(t, got, underlying)
	})
}
