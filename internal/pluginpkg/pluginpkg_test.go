package pluginpkg_test

import (
	"archive/tar"
	"compress/gzip"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/justenwalker/kevin/internal/pluginpkg"
)

// tarEntry is one file or directory to write into a test fixture archive.
type tarEntry struct {
	name string
	dir  bool
	mode int64
	data []byte
}

// writeTar builds a fixture archive at path from entries, gzip-compressed
// when gz is true.
func writeTar(t *testing.T, path string, gz bool, entries []tarEntry) {
	t.Helper()
	f, err := os.Create(path)
	require.NoError(t, err)

	var w io.Writer = f
	var gzw *gzip.Writer
	if gz {
		gzw = gzip.NewWriter(f)
		w = gzw
	}

	tw := tar.NewWriter(w)
	for _, e := range entries {
		hdr := &tar.Header{Name: e.name, Mode: e.mode}
		if e.dir {
			hdr.Typeflag = tar.TypeDir
		} else {
			hdr.Typeflag = tar.TypeReg
			hdr.Size = int64(len(e.data))
		}
		require.NoError(t, tw.WriteHeader(hdr))
		if !e.dir {
			_, err := tw.Write(e.data)
			require.NoError(t, err)
		}
	}
	require.NoError(t, tw.Close())
	if gzw != nil {
		require.NoError(t, gzw.Close())
	}
	require.NoError(t, f.Close())
}

// manifestEntry builds a manifest.json tarEntry, starting from a valid
// manifest and applying edit to it.
func manifestEntry(t *testing.T, edit func(*pluginpkg.Manifest)) tarEntry {
	t.Helper()
	m := pluginpkg.Manifest{
		ManifestVersion: pluginpkg.CurrentManifestVersion,
		Name:            "acme",
		Version:         "1.0.0",
		Entrypoint:      "bin/acme",
	}
	if edit != nil {
		edit(&m)
	}
	data, err := json.Marshal(m)
	require.NoError(t, err)
	return tarEntry{name: pluginpkg.ManifestFile, data: data, mode: 0o644}
}

// validPackage builds a fixture archive with a valid manifest and a
// non-executable entrypoint, and returns its path.
func validPackage(t *testing.T, dir string, gz bool) string {
	t.Helper()
	name := "pkg.tar"
	if gz {
		name = "pkg.tar.gz"
	}
	path := filepath.Join(dir, name)
	writeTar(t, path, gz, []tarEntry{
		manifestEntry(t, nil),
		{name: "bin/acme", data: []byte("#!/bin/sh\necho hi\n"), mode: 0o644},
	})
	return path
}

// extractErr writes entries as a tar fixture and returns the error Extract
// reports for it.
func extractErr(t *testing.T, entries []tarEntry) error {
	t.Helper()
	dir := t.TempDir()
	pkgPath := filepath.Join(dir, "pkg.tar")
	writeTar(t, pkgPath, false, entries)
	_, err := pluginpkg.Extract(pkgPath, filepath.Join(dir, "dest"), "")
	return err
}

func sha256Of(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	require.NoError(t, err)
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

func TestExtract(t *testing.T) {
	t.Run("decodes a valid tar.gz package", func(t *testing.T) {
		dir := t.TempDir()
		pkgPath := validPackage(t, dir, true)
		destDir := filepath.Join(dir, "dest")

		result, err := pluginpkg.Extract(pkgPath, destDir, "")
		require.NoError(t, err)
		assert.Equal(t, "acme", result.Name)
		assert.Equal(t, "1.0.0", result.Version)
		assert.Equal(t, filepath.Join(destDir, "bin/acme"), result.Cmd)
	})

	t.Run("decodes a valid plain tar package", func(t *testing.T) {
		dir := t.TempDir()
		pkgPath := validPackage(t, dir, false)
		destDir := filepath.Join(dir, "dest")

		result, err := pluginpkg.Extract(pkgPath, destDir, "")
		require.NoError(t, err)
		assert.Equal(t, filepath.Join(destDir, "bin/acme"), result.Cmd)
	})

	t.Run("makes the entrypoint executable", func(t *testing.T) {
		dir := t.TempDir()
		pkgPath := validPackage(t, dir, true) // entrypoint written with mode 0o644
		destDir := filepath.Join(dir, "dest")

		result, err := pluginpkg.Extract(pkgPath, destDir, "")
		require.NoError(t, err)

		info, err := os.Stat(result.Cmd)
		require.NoError(t, err)
		assert.NotZero(t, info.Mode()&0o111, "entrypoint should be executable after extraction")
	})

	t.Run("fails when the manifest is missing", func(t *testing.T) {
		err := extractErr(t, []tarEntry{{name: "bin/acme", data: []byte("x"), mode: 0o755}})
		assert.ErrorIs(t, err, pluginpkg.ErrManifestMissing)
	})

	t.Run("fails when the manifest is not valid JSON", func(t *testing.T) {
		err := extractErr(t, []tarEntry{
			{name: pluginpkg.ManifestFile, data: []byte("{not json"), mode: 0o644},
		})
		assert.ErrorIs(t, err, pluginpkg.ErrManifestInvalid)
	})

	t.Run("rejects an unsafe entrypoint", func(t *testing.T) {
		tests := []struct {
			name       string
			entrypoint string
		}{
			{name: "empty", entrypoint: ""},
			{name: "absolute", entrypoint: "/etc/passwd"},
			{name: "traversal", entrypoint: "../../etc/passwd"},
		}
		for _, tt := range tests {
			t.Run(tt.name, func(t *testing.T) {
				err := extractErr(t, []tarEntry{
					manifestEntry(t, func(m *pluginpkg.Manifest) { m.Entrypoint = tt.entrypoint }),
				})
				assert.ErrorIs(t, err, pluginpkg.ErrManifestInvalid)
			})
		}
	})

	t.Run("requires a name and a version", func(t *testing.T) {
		tests := []struct {
			name string
			edit func(*pluginpkg.Manifest)
		}{
			{name: "empty name", edit: func(m *pluginpkg.Manifest) { m.Name = "" }},
			{name: "empty version", edit: func(m *pluginpkg.Manifest) { m.Version = "" }},
		}
		for _, tt := range tests {
			t.Run(tt.name, func(t *testing.T) {
				err := extractErr(t, []tarEntry{manifestEntry(t, tt.edit)})
				assert.ErrorIs(t, err, pluginpkg.ErrManifestInvalid)
			})
		}
	})

	t.Run("rejects an unsupported manifest version", func(t *testing.T) {
		tests := []struct {
			name string
			v    int
		}{
			{name: "missing (zero value)", v: 0},
			{name: "future version", v: pluginpkg.CurrentManifestVersion + 1},
		}
		for _, tt := range tests {
			t.Run(tt.name, func(t *testing.T) {
				err := extractErr(t, []tarEntry{
					manifestEntry(t, func(m *pluginpkg.Manifest) { m.ManifestVersion = tt.v }),
				})
				assert.ErrorIs(t, err, pluginpkg.ErrManifestVersion)
			})
		}
	})

	t.Run("fails when the manifest names a missing entrypoint", func(t *testing.T) {
		err := extractErr(t, []tarEntry{
			manifestEntry(t, func(m *pluginpkg.Manifest) { m.Entrypoint = "bin/nowhere" }),
		})
		assert.ErrorIs(t, err, pluginpkg.ErrEntrypointMissing)
	})

	t.Run("rejects a corrupt archive", func(t *testing.T) {
		tests := []struct {
			name string
			data []byte
		}{
			{name: "corrupt gzip header", data: []byte{0x1f, 0x8b, 0xff, 0xff, 0xff}},
			{name: "corrupt tar body", data: []byte("not a tar archive at all, just text")},
		}
		for _, tt := range tests {
			t.Run(tt.name, func(t *testing.T) {
				dir := t.TempDir()
				pkgPath := filepath.Join(dir, "pkg.tar")
				require.NoError(t, os.WriteFile(pkgPath, tt.data, 0o644))

				_, err := pluginpkg.Extract(pkgPath, filepath.Join(dir, "dest"), "")
				assert.ErrorIs(t, err, pluginpkg.ErrBadArchive)
			})
		}
	})

	t.Run("rejects a tar entry that escapes destDir", func(t *testing.T) {
		err := extractErr(t, []tarEntry{
			manifestEntry(t, nil),
			{name: "../escape", data: []byte("x"), mode: 0o644},
		})
		assert.ErrorIs(t, err, pluginpkg.ErrUnsafePath)
	})

	t.Run("verifies an expected checksum", func(t *testing.T) {
		dir := t.TempDir()
		pkgPath := validPackage(t, dir, true)
		sum := sha256Of(t, pkgPath)

		t.Run("matching checksum succeeds", func(t *testing.T) {
			_, err := pluginpkg.Extract(pkgPath, filepath.Join(dir, "dest-ok"), "sha256:"+sum)
			require.NoError(t, err)
		})

		t.Run("mismatched checksum fails", func(t *testing.T) {
			_, err := pluginpkg.Extract(pkgPath, filepath.Join(dir, "dest-bad"), "sha256:0000000000000000000000000000000000000000000000000000000000000000")
			assert.ErrorIs(t, err, pluginpkg.ErrChecksumMismatch)
		})
	})

	t.Run("skips re-extraction when the archive is unchanged", func(t *testing.T) {
		dir := t.TempDir()
		pkgPath := validPackage(t, dir, true)
		destDir := filepath.Join(dir, "dest")

		_, err := pluginpkg.Extract(pkgPath, destDir, "")
		require.NoError(t, err)

		sentinel := filepath.Join(destDir, "untouched")
		require.NoError(t, os.WriteFile(sentinel, []byte("still here"), 0o600))

		result, err := pluginpkg.Extract(pkgPath, destDir, "")
		require.NoError(t, err)
		assert.Equal(t, "acme", result.Name)
		assert.FileExists(t, sentinel, "a cache hit must not have cleared destDir")
	})

	t.Run("re-extracts when the archive content changes", func(t *testing.T) {
		dir := t.TempDir()
		pkgPath := filepath.Join(dir, "pkg.tar")
		destDir := filepath.Join(dir, "dest")

		writeTar(t, pkgPath, false, []tarEntry{
			manifestEntry(t, nil),
			{name: "bin/acme", data: []byte("v1"), mode: 0o755},
		})
		_, err := pluginpkg.Extract(pkgPath, destDir, "")
		require.NoError(t, err)

		sentinel := filepath.Join(destDir, "untouched")
		require.NoError(t, os.WriteFile(sentinel, []byte("still here"), 0o600))

		writeTar(t, pkgPath, false, []tarEntry{
			manifestEntry(t, func(m *pluginpkg.Manifest) { m.Version = "2.0.0" }),
			{name: "bin/acme", data: []byte("v2, different size"), mode: 0o755},
		})
		result, err := pluginpkg.Extract(pkgPath, destDir, "")
		require.NoError(t, err)
		assert.Equal(t, "2.0.0", result.Version)
		assert.NoFileExists(t, sentinel, "a changed archive must force a real re-extraction")
	})

	t.Run("ignores a stale cache marker when extracted files are missing", func(t *testing.T) {
		dir := t.TempDir()
		pkgPath := validPackage(t, dir, true)
		destDir := filepath.Join(dir, "dest")

		_, err := pluginpkg.Extract(pkgPath, destDir, "")
		require.NoError(t, err)

		require.NoError(t, os.Remove(filepath.Join(destDir, "bin", "acme")))

		result, err := pluginpkg.Extract(pkgPath, destDir, "")
		require.NoError(t, err)
		assert.FileExists(t, result.Cmd, "a missing entrypoint despite a matching marker must trigger a real re-extraction")
	})
}

func TestPack(t *testing.T) {
	t.Run("uses an existing manifest.json as-is", func(t *testing.T) {
		srcDir := t.TempDir()
		writeManifest(t, srcDir, pluginpkg.Manifest{Name: "acme", Version: "1.0.0", Entrypoint: "bin/acme"})
		require.NoError(t, os.Mkdir(filepath.Join(srcDir, "bin"), 0o755))
		require.NoError(t, os.WriteFile(filepath.Join(srcDir, "bin", "acme"), []byte("x"), 0o755))

		outPath := filepath.Join(t.TempDir(), "pkg.tar.gz")
		manifest, err := pluginpkg.Pack(srcDir, outPath, pluginpkg.Manifest{})
		require.NoError(t, err)
		assert.Equal(t, "acme", manifest.Name)
		assert.Equal(t, "1.0.0", manifest.Version)
		assert.FileExists(t, outPath)
	})

	t.Run("overlay overrides an existing manifest.json's fields", func(t *testing.T) {
		srcDir := t.TempDir()
		writeManifest(t, srcDir, pluginpkg.Manifest{Name: "acme", Version: "1.0.0", Entrypoint: "bin/acme"})
		require.NoError(t, os.Mkdir(filepath.Join(srcDir, "bin"), 0o755))
		require.NoError(t, os.WriteFile(filepath.Join(srcDir, "bin", "acme"), []byte("x"), 0o755))

		outPath := filepath.Join(t.TempDir(), "pkg.tar.gz")
		manifest, err := pluginpkg.Pack(srcDir, outPath, pluginpkg.Manifest{Version: "2.0.0"})
		require.NoError(t, err)
		assert.Equal(t, "acme", manifest.Name, "unset overlay fields must not clobber the manifest.json value")
		assert.Equal(t, "2.0.0", manifest.Version)
	})

	t.Run("overlay alone is enough when srcDir has no manifest.json", func(t *testing.T) {
		srcDir := t.TempDir()
		require.NoError(t, os.WriteFile(filepath.Join(srcDir, "acme"), []byte("x"), 0o644))

		outPath := filepath.Join(t.TempDir(), "pkg.tar.gz")
		manifest, err := pluginpkg.Pack(srcDir, outPath, pluginpkg.Manifest{Name: "acme", Version: "1.0.0", Entrypoint: "acme"})
		require.NoError(t, err)
		assert.Equal(t, "acme", manifest.Name)
	})

	t.Run("fails when name, version or entrypoint end up unset", func(t *testing.T) {
		srcDir := t.TempDir()
		outPath := filepath.Join(t.TempDir(), "pkg.tar.gz")
		_, err := pluginpkg.Pack(srcDir, outPath, pluginpkg.Manifest{Name: "acme"})
		assert.ErrorIs(t, err, pluginpkg.ErrManifestInvalid)
	})

	t.Run("fails when the entrypoint does not exist in srcDir", func(t *testing.T) {
		srcDir := t.TempDir()
		outPath := filepath.Join(t.TempDir(), "pkg.tar.gz")
		_, err := pluginpkg.Pack(srcDir, outPath, pluginpkg.Manifest{Name: "acme", Version: "1.0.0", Entrypoint: "bin/acme"})
		assert.ErrorIs(t, err, pluginpkg.ErrEntrypointMissing)
	})

	t.Run("leaves no partial file behind when writing the archive fails", func(t *testing.T) {
		srcDir := t.TempDir()
		require.NoError(t, os.WriteFile(filepath.Join(srcDir, "acme"), []byte("x"), 0o755))
		// A directory in place of outPath makes the final os.Rename fail
		// after the archive has already been built in a temp file.
		outDir := t.TempDir()
		outPath := filepath.Join(outDir, "pkg.tar.gz")
		require.NoError(t, os.Mkdir(outPath, 0o755))

		_, err := pluginpkg.Pack(srcDir, outPath, pluginpkg.Manifest{Name: "acme", Version: "1.0.0", Entrypoint: "acme"})
		require.Error(t, err)

		entries, err := os.ReadDir(outPath)
		require.NoError(t, err)
		assert.Empty(t, entries, "outPath must be untouched, since the rename onto it failed")

		siblings, err := os.ReadDir(outDir)
		require.NoError(t, err)
		assert.Len(t, siblings, 1, "the temp file used to build the archive must be cleaned up, leaving only the outPath directory")
	})

	t.Run("rejects an entrypoint that escapes srcDir", func(t *testing.T) {
		srcDir := t.TempDir()
		outPath := filepath.Join(t.TempDir(), "pkg.tar.gz")
		_, err := pluginpkg.Pack(srcDir, outPath, pluginpkg.Manifest{Name: "acme", Version: "1.0.0", Entrypoint: "../../etc/passwd"})
		assert.ErrorIs(t, err, pluginpkg.ErrManifestInvalid)
	})

	t.Run("round-trips through Extract", func(t *testing.T) {
		srcDir := t.TempDir()
		require.NoError(t, os.Mkdir(filepath.Join(srcDir, "bin"), 0o755))
		require.NoError(t, os.WriteFile(filepath.Join(srcDir, "bin", "acme"), []byte("#!/bin/sh\necho hi\n"), 0o644)) // no exec bit
		require.NoError(t, os.WriteFile(filepath.Join(srcDir, "README.md"), []byte("hi"), 0o644))

		outPath := filepath.Join(t.TempDir(), "pkg.tar.gz")
		_, err := pluginpkg.Pack(srcDir, outPath, pluginpkg.Manifest{
			Name: "acme", Version: "1.0.0", Entrypoint: "bin/acme", Args: []string{"--flag"},
		})
		require.NoError(t, err)

		destDir := filepath.Join(t.TempDir(), "dest")
		result, err := pluginpkg.Extract(outPath, destDir, "")
		require.NoError(t, err)
		assert.Equal(t, "acme", result.Name)
		assert.Equal(t, "1.0.0", result.Version)
		assert.Equal(t, []string{"--flag"}, result.Args)
		assert.FileExists(t, filepath.Join(destDir, "README.md"))

		info, err := os.Stat(result.Cmd)
		require.NoError(t, err)
		assert.NotZero(t, info.Mode()&0o111, "the entrypoint must come out executable even though the source file wasn't")
	})
}

// writeManifest writes m as srcDir's manifest.json.
func writeManifest(t *testing.T, srcDir string, m pluginpkg.Manifest) {
	t.Helper()
	m.ManifestVersion = pluginpkg.CurrentManifestVersion
	data, err := json.Marshal(m)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(filepath.Join(srcDir, pluginpkg.ManifestFile), data, 0o644))
}
