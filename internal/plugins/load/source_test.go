package load

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/justenwalker/kevin/internal/command"
	"github.com/justenwalker/kevin/internal/config"
	"github.com/justenwalker/kevin/internal/plugins/signing"
	"github.com/justenwalker/kevin/internal/plugins/source/cache"
)

func TestSourceFor(t *testing.T) {
	t.Run("file joins a relative path onto the project directory", func(t *testing.T) {
		src := sourceFor(config.PluginSpec{File: "pkg/acme.tar", Checksum: "sha256:abc"}, "/work")
		assert.Equal(t, fileSource{path: "/work/pkg/acme.tar", checksum: "sha256:abc"}, src)
	})

	t.Run("file keeps an absolute path", func(t *testing.T) {
		src := sourceFor(config.PluginSpec{File: "/opt/acme.tar"}, "/work")
		assert.Equal(t, fileSource{path: "/opt/acme.tar"}, src)
	})

	t.Run("oci and http", func(t *testing.T) {
		assert.Equal(t, ociSource{ref: "ghcr.io/acme/p:v1"}, sourceFor(config.PluginSpec{OCI: "ghcr.io/acme/p:v1"}, "/work"))
		assert.Equal(t,
			httpSource{url: "https://x.test/p.tar", checksum: "sha256:abc"},
			sourceFor(config.PluginSpec{HTTP: "https://x.test/p.tar", Checksum: "sha256:abc"}, "/work"))
	})

	t.Run("a cmd entry has no source", func(t *testing.T) {
		assert.Nil(t, sourceFor(config.PluginSpec{Cmd: "./plugin"}, "/work"))
	})
}

func TestFileSourceSignature(t *testing.T) {
	pkg := filepath.Join(t.TempDir(), "acme.tar")
	src := fileSource{path: pkg}
	entry, err := src.fetch(t.Context())
	require.NoError(t, err)

	t.Run("a missing signature file is ErrSignatureMissing", func(t *testing.T) {
		_, err := src.signature(t.Context(), entry, config.SigningSchemeMinisign)
		require.ErrorIs(t, err, signing.ErrSignatureMissing)
	})

	t.Run("reads the suffix the scheme names", func(t *testing.T) {
		require.NoError(t, os.WriteFile(pkg+".minisig", []byte("mini"), 0o600))
		require.NoError(t, os.WriteFile(pkg+".sigstore.json", []byte("bundle"), 0o600))

		got, err := src.signature(t.Context(), entry, config.SigningSchemeMinisign)
		require.NoError(t, err)
		assert.Equal(t, "mini", string(got))

		got, err = src.signature(t.Context(), entry, config.SigningSchemeSigstore)
		require.NoError(t, err)
		assert.Equal(t, "bundle", string(got))
	})
}

// fakeSource returns canned signature bytes.
type fakeSource struct {
	sig []byte
	err error
}

func (fakeSource) fetch(context.Context) (cache.Entry, error) { return cache.Entry{}, nil }

func (s fakeSource) signature(context.Context, cache.Entry, config.SigningScheme) ([]byte, error) {
	return s.sig, s.err
}

// recordingRunner records the command it is asked to run instead of running it.
type recordingRunner struct {
	cmd *exec.Cmd
}

func (r *recordingRunner) Run(_ context.Context, cmd *exec.Cmd) error {
	r.cmd = cmd
	return nil
}

func TestVerifySource(t *testing.T) {
	t.Run("no signing block skips the check", func(t *testing.T) {
		require.NoError(t, verifySource(t.Context(), fakeSource{err: signing.ErrSignatureMissing}, cache.Entry{}, nil, command.Default))
	})

	t.Run("a signature lookup failure is returned", func(t *testing.T) {
		spec := &config.SigningSpec{Scheme: config.SigningSchemeMinisign}
		err := verifySource(t.Context(), fakeSource{err: signing.ErrSignatureMissing}, cache.Entry{}, spec, command.Default)
		require.ErrorIs(t, err, signing.ErrSignatureMissing)
	})

	t.Run("a malformed minisign signature is invalid", func(t *testing.T) {
		t.Setenv("HOME", t.TempDir())
		t.Setenv("KEVIN_USER_STATE_DIR", "")
		spec := &config.SigningSpec{Scheme: config.SigningSchemeMinisign}
		err := verifySource(t.Context(), fakeSource{sig: []byte("garbage")}, cache.Entry{}, spec, command.Default)
		require.ErrorIs(t, err, signing.ErrSignatureInvalid)
	})

	t.Run("sigstore runs cosign with the signing identity through the runner", func(t *testing.T) {
		t.Setenv("HOME", t.TempDir())
		t.Setenv("KEVIN_USER_STATE_DIR", "")
		require.NoError(t, signing.AddIdentity("ci@acme.example", "https://issuer.example"))

		runner := &recordingRunner{}

		spec := &config.SigningSpec{
			Scheme: config.SigningSchemeSigstore, Identity: "ci@acme.example", Issuer: "https://issuer.example",
		}
		err := verifySource(t.Context(), fakeSource{sig: []byte("{}")}, cache.Entry{Path: "/cache/pkg"}, spec, runner)
		require.NoError(t, err)
		require.NotNil(t, runner.cmd)
		assert.Contains(t, runner.cmd.Args, "ci@acme.example")
		assert.Contains(t, runner.cmd.Args, "https://issuer.example")
		assert.Equal(t, "/cache/pkg", runner.cmd.Args[len(runner.cmd.Args)-1])
	})
}
