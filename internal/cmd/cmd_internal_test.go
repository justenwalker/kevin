package cmd

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"

	"github.com/spf13/cobra"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/justenwalker/kevin/internal/ocipkg"
	"github.com/justenwalker/kevin/protos/pb"
)

func TestPrintEnvironmentInfo(t *testing.T) {
	var buf bytes.Buffer
	printEnvironmentInfo(&buf, &pb.Environment{
		ConsoleAddr:   "127.0.0.1:18081",
		HttpProxyAddr: "127.0.0.1:18080",
	})

	out := buf.String()
	assert.Contains(t, out, "http://127.0.0.1:18081")
	assert.Contains(t, out, "http://127.0.0.1:18080")
	assert.Contains(t, out, "http://127.0.0.1:18081/_mcp")
	assert.Contains(t, out, "export HTTP_PROXY=http://127.0.0.1:18080 HTTPS_PROXY=http://127.0.0.1:18080")
	assert.Contains(t, out, "claude mcp add --transport http kevin http://127.0.0.1:18081/_mcp")
}

func TestPushSignatureIfPresent(t *testing.T) {
	newTestCmd := func() (*cobra.Command, *bytes.Buffer) {
		var buf bytes.Buffer
		c := &cobra.Command{}
		c.SetOut(&buf)
		return c, &buf
	}

	t.Run("hints at both schemes when no sibling file exists", func(t *testing.T) {
		tarPath := filepath.Join(t.TempDir(), "pkg.tar.gz")
		require.NoError(t, os.WriteFile(tarPath, []byte("x"), 0o600))
		c, buf := newTestCmd()

		require.NoError(t, pushSignatureIfPresent(c, "registry.example.com/acme/plugin:v1", tarPath))

		assert.Contains(t, buf.String(), "minisign -Sm "+tarPath)
		assert.Contains(t, buf.String(), "cosign sign-blob --bundle "+tarPath+".sigstore.json "+tarPath)
	})

	t.Run("stops at the first sibling push attempts hit", func(t *testing.T) {
		tarPath := filepath.Join(t.TempDir(), "pkg.tar.gz")
		require.NoError(t, os.WriteFile(tarPath, []byte("x"), 0o600))
		require.NoError(t, os.WriteFile(tarPath+".minisig", []byte("sig"), 0o600))
		require.NoError(t, os.WriteFile(tarPath+".sigstore.json", []byte("{}"), 0o600))
		c, _ := newTestCmd()

		// An invalid ref fails at ociref.Parse before any network call, so
		// this proves a found sibling was actually pushed, rather than
		// silently skipped.
		err := pushSignatureIfPresent(c, "not a valid ref!!", tarPath)
		require.ErrorIs(t, err, ocipkg.ErrBadReference)
	})
}

func TestFoundSignatureSiblings(t *testing.T) {
	t.Run("reports no siblings when neither file exists", func(t *testing.T) {
		tarPath := filepath.Join(t.TempDir(), "pkg.tar.gz")
		found, err := foundSignatureSiblings(tarPath)
		require.NoError(t, err)
		assert.Empty(t, found)
	})

	t.Run("finds a minisig sibling alone", func(t *testing.T) {
		tarPath := filepath.Join(t.TempDir(), "pkg.tar.gz")
		require.NoError(t, os.WriteFile(tarPath+".minisig", []byte("sig"), 0o600))

		found, err := foundSignatureSiblings(tarPath)
		require.NoError(t, err)
		require.Len(t, found, 1)
		assert.Equal(t, ocipkg.SignatureMediaType, found[0].mediaType)
	})

	t.Run("finds a sigstore sibling alone", func(t *testing.T) {
		tarPath := filepath.Join(t.TempDir(), "pkg.tar.gz")
		require.NoError(t, os.WriteFile(tarPath+".sigstore.json", []byte("{}"), 0o600))

		found, err := foundSignatureSiblings(tarPath)
		require.NoError(t, err)
		require.Len(t, found, 1)
		assert.Equal(t, ocipkg.SigstoreSignatureMediaType, found[0].mediaType)
	})

	t.Run("finds both siblings when both exist", func(t *testing.T) {
		tarPath := filepath.Join(t.TempDir(), "pkg.tar.gz")
		require.NoError(t, os.WriteFile(tarPath+".minisig", []byte("sig"), 0o600))
		require.NoError(t, os.WriteFile(tarPath+".sigstore.json", []byte("{}"), 0o600))

		found, err := foundSignatureSiblings(tarPath)
		require.NoError(t, err)
		require.Len(t, found, 2)
		assert.Equal(t, ocipkg.SignatureMediaType, found[0].mediaType)
		assert.Equal(t, ocipkg.SigstoreSignatureMediaType, found[1].mediaType)
	})
}
