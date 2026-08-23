// Package pluginpkg builds and extracts a kevin plugin package: a tar
// archive, optionally gzip-compressed, holding a manifest.json, an
// executable entrypoint, and any supporting files it needs alongside it.
package pluginpkg

import (
	"archive/tar"
	"bufio"
	"compress/gzip"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
)

// ManifestFile is the fixed name of a package's manifest, at the tar root.
const ManifestFile = "manifest.json"

// CurrentManifestVersion is the only manifest $v Extract understands.
const CurrentManifestVersion = 1

// sumFile records the sha256 of the archive that produced a destination
// directory's contents, so a later Extract of the same bytes can skip
// re-extracting. It is a cache, not tracked state: deleting it, or the
// directory it lives in, only costs one re-extraction.
const sumFile = ".kevin-pkg-sum"

// Manifest is the decoded manifest.json at a package's tar root.
type Manifest struct {
	// ManifestVersion is the manifest.json's $v field.
	ManifestVersion int `json:"$v"`
	// Name is the plugin's name.
	Name string `json:"name"`
	// Version is the plugin's version.
	Version string `json:"version"`
	// Author is the plugin's author, or "" when unset.
	Author string `json:"author,omitempty"`
	// Description is the plugin's description, or "" when unset.
	Description string `json:"description,omitempty"`
	// Entrypoint is the executable's path, relative to the package root.
	Entrypoint string `json:"entrypoint"`
	// Args are the fixed arguments to launch the entrypoint with.
	Args []string `json:"args,omitempty"`
}

// Result is what Extract resolves into a launchable spec.
type Result struct {
	// Name is the plugin's name.
	Name string
	// Version is the plugin's version.
	Version string
	// Cmd is the entrypoint's absolute path.
	Cmd string
	// Args are the fixed arguments to launch Cmd with.
	Args []string
}

// Extract verifies pkgPath against checksum (if non-empty, "sha256:<hex>"),
// then extracts it into destDir and returns the resolved entrypoint.
func Extract(pkgPath, destDir, checksum string) (Result, error) {
	f, err := os.Open(pkgPath) //nolint:gosec // pkgPath names a configured plugin package, the whole point
	if err != nil {
		return Result{}, fmt.Errorf("pluginpkg: open %q: %w", pkgPath, err)
	}
	defer f.Close() //nolint:errcheck // read-only handle, nothing to flush

	sum, err := hashFile(f)
	if err != nil {
		return Result{}, fmt.Errorf("pluginpkg: hash %q: %w", pkgPath, err)
	}
	if checksum != "" && !checksumMatches(sum, checksum) {
		return Result{}, fmt.Errorf("pluginpkg: %q: %w", pkgPath, ErrChecksumMismatch)
	}

	if result, ok := cached(destDir, sum); ok {
		return result, nil
	}

	if _, err = f.Seek(0, io.SeekStart); err != nil {
		return Result{}, fmt.Errorf("pluginpkg: seek %q: %w", pkgPath, err)
	}
	manifest, err := extractTo(f, destDir)
	if err != nil {
		return Result{}, err
	}
	if err := os.WriteFile(filepath.Join(destDir, sumFile), []byte(sum), 0o600); err != nil {
		return Result{}, fmt.Errorf("pluginpkg: write cache marker: %w", err)
	}

	return resultFrom(manifest, destDir), nil
}

// Pack builds a plugin package - a gzip-compressed tar holding a
// manifest.json, the entrypoint, and any supporting files - from srcDir and
// writes it to outPath. overlay's non-zero fields (Name, Version, Author,
// Description, Entrypoint, and a non-nil Args) replace the matching field
// of srcDir's own manifest.json, if any; overlay alone must be enough when
// srcDir has none. Pack returns the manifest it packed.
func Pack(srcDir, outPath string, overlay Manifest) (Manifest, error) {
	manifest, err := mergedManifest(srcDir, overlay)
	if err != nil {
		return Manifest{}, err
	}
	entrypoint, err := safeJoin(srcDir, manifest.Entrypoint)
	if err != nil {
		return Manifest{}, fmt.Errorf("pluginpkg: manifest entrypoint: %w", ErrManifestInvalid)
	}
	if info, statErr := os.Stat(entrypoint); statErr != nil || info.IsDir() {
		return Manifest{}, fmt.Errorf("pluginpkg: %q: %w", manifest.Entrypoint, ErrEntrypointMissing)
	}

	if err := writePackage(outPath, srcDir, manifest); err != nil {
		return Manifest{}, err
	}
	return manifest, nil
}

// mergedManifest reads srcDir's manifest.json, if any, and applies overlay's
// non-zero fields on top of it, then validates the result.
func mergedManifest(srcDir string, overlay Manifest) (Manifest, error) {
	m := Manifest{ManifestVersion: CurrentManifestVersion}
	data, err := os.ReadFile(filepath.Join(srcDir, ManifestFile)) //nolint:gosec // srcDir is a caller-chosen plugin source directory, not user input
	switch {
	case err == nil:
		if jsonErr := json.Unmarshal(data, &m); jsonErr != nil {
			return Manifest{}, fmt.Errorf("pluginpkg: %w: %w", ErrManifestInvalid, jsonErr)
		}
	case !errors.Is(err, os.ErrNotExist):
		return Manifest{}, fmt.Errorf("pluginpkg: read manifest: %w", err)
	}
	m.ManifestVersion = CurrentManifestVersion

	if overlay.Name != "" {
		m.Name = overlay.Name
	}
	if overlay.Version != "" {
		m.Version = overlay.Version
	}
	if overlay.Author != "" {
		m.Author = overlay.Author
	}
	if overlay.Description != "" {
		m.Description = overlay.Description
	}
	if overlay.Entrypoint != "" {
		m.Entrypoint = overlay.Entrypoint
	}
	if overlay.Args != nil {
		m.Args = overlay.Args
	}

	if m.Name == "" || m.Version == "" || m.Entrypoint == "" {
		return Manifest{}, fmt.Errorf("pluginpkg: name, version and entrypoint are required: %w", ErrManifestInvalid)
	}
	return m, nil
}

// writePackage writes a gzip-compressed tar of manifest plus every file
// under srcDir (except ManifestFile and sumFile, which are not source) to
// outPath. It builds the archive in a temp file next to outPath and renames
// it into place only once complete, so a failure midway never leaves a
// truncated file at outPath.
func writePackage(outPath, srcDir string, manifest Manifest) error {
	dir := filepath.Dir(outPath)
	tmp, err := os.CreateTemp(dir, "*.tmp") // same filesystem as outPath, for an atomic rename
	if err != nil {
		return fmt.Errorf("pluginpkg: create temp file: %w", err)
	}
	defer os.Remove(tmp.Name()) //nolint:errcheck // no-op once renamed away

	if err := buildTarGz(tmp, srcDir, manifest); err != nil {
		tmp.Close() //nolint:errcheck,gosec // best effort; the build error above is what's reported
		return err
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("pluginpkg: close temp file: %w", err)
	}
	if err := os.Rename(tmp.Name(), outPath); err != nil {
		return fmt.Errorf("pluginpkg: %w", err)
	}
	return nil
}

// buildTarGz writes a gzip-compressed tar of manifest plus srcDir's content
// to w.
func buildTarGz(w io.Writer, srcDir string, manifest Manifest) error {
	gz := gzip.NewWriter(w)
	tw := tar.NewWriter(gz)
	if err := writeManifestEntry(tw, manifest); err != nil {
		return err
	}
	if err := packDir(tw, srcDir, manifest.Entrypoint); err != nil {
		return err
	}
	if err := tw.Close(); err != nil {
		return fmt.Errorf("pluginpkg: close tar writer: %w", err)
	}
	if err := gz.Close(); err != nil {
		return fmt.Errorf("pluginpkg: close gzip writer: %w", err)
	}
	return nil
}

// writeManifestEntry writes m, JSON-encoded, as ManifestFile at the tar
// root.
func writeManifestEntry(tw *tar.Writer, m Manifest) error {
	data, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return fmt.Errorf("pluginpkg: encode manifest: %w", err)
	}
	hdr := &tar.Header{Name: ManifestFile, Mode: 0o644, Size: int64(len(data)), Typeflag: tar.TypeReg}
	if err := tw.WriteHeader(hdr); err != nil {
		return fmt.Errorf("pluginpkg: write manifest header: %w", err)
	}
	if _, err := tw.Write(data); err != nil {
		return fmt.Errorf("pluginpkg: write manifest: %w", err)
	}
	return nil
}

// packDir writes every regular file and directory under srcDir into tw,
// relative to srcDir, skipping ManifestFile and sumFile. Symlinks, devices,
// and other non-regular entries are skipped. entrypointRel's tar mode gets
// the exec bit forced on.
func packDir(tw *tar.Writer, srcDir, entrypointRel string) error {
	if err := filepath.WalkDir(srcDir, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return fmt.Errorf("walk %q: %w", path, err)
		}
		rel, err := filepath.Rel(srcDir, path)
		if err != nil {
			return fmt.Errorf("%q: %w", path, err)
		}
		if rel == "." {
			return nil
		}
		rel = filepath.ToSlash(rel)
		if rel == ManifestFile || rel == sumFile {
			return nil
		}

		if d.IsDir() {
			if hdrErr := tw.WriteHeader(&tar.Header{Name: rel + "/", Mode: 0o755, Typeflag: tar.TypeDir}); hdrErr != nil {
				return fmt.Errorf("write header %q: %w", rel, hdrErr)
			}
			return nil
		}
		info, err := d.Info()
		if err != nil {
			return fmt.Errorf("stat %q: %w", path, err)
		}
		if !info.Mode().IsRegular() {
			return nil
		}
		return packFile(tw, path, rel, info, rel == entrypointRel)
	}); err != nil {
		return fmt.Errorf("pluginpkg: %w", err)
	}
	return nil
}

// packFile writes path's content into tw as a tar.TypeReg entry named rel.
// When forceExec is set, the entry's mode gets the exec bit forced on.
func packFile(tw *tar.Writer, path, rel string, info fs.FileInfo, forceExec bool) error {
	mode := info.Mode().Perm()
	if forceExec {
		mode |= 0o111
	}
	if err := tw.WriteHeader(&tar.Header{Name: rel, Mode: int64(mode), Size: info.Size(), Typeflag: tar.TypeReg}); err != nil {
		return fmt.Errorf("write header %q: %w", rel, err)
	}

	f, err := os.Open(path) //nolint:gosec // path comes from WalkDir over a caller-chosen srcDir, not user input
	if err != nil {
		return fmt.Errorf("open %q: %w", path, err)
	}
	defer f.Close() //nolint:errcheck // read-only handle, nothing to flush

	if _, err := io.Copy(tw, f); err != nil {
		return fmt.Errorf("write %q: %w", rel, err)
	}
	return nil
}

func hashFile(f *os.File) (string, error) {
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", fmt.Errorf("pluginpkg: %w", err)
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

func checksumMatches(sum, want string) bool {
	hex, ok := strings.CutPrefix(want, "sha256:")
	return ok && strings.EqualFold(hex, sum)
}

// cached reports whether destDir already holds the result of extracting the
// archive with digest sum. Anything short of an exact match is a miss.
func cached(destDir, sum string) (Result, bool) {
	marker, err := os.ReadFile(filepath.Join(destDir, sumFile)) //nolint:gosec // destDir is a kevin-managed workspace path, not user input
	if err != nil || string(marker) != sum {
		return Result{}, false
	}
	manifest, err := readManifest(destDir)
	if err != nil {
		return Result{}, false
	}
	if _, err := os.Stat(filepath.Join(destDir, manifest.Entrypoint)); err != nil {
		return Result{}, false
	}
	return resultFrom(manifest, destDir), true
}

// extractTo replaces destDir with pkg's contents - gzip-decompressing first
// if pkg is compressed - and returns the extracted manifest.
func extractTo(pkg *os.File, destDir string) (Manifest, error) {
	br := bufio.NewReader(pkg)
	var r io.Reader = br
	if magic, err := br.Peek(2); err == nil && magic[0] == 0x1f && magic[1] == 0x8b {
		gz, err := gzip.NewReader(br)
		if err != nil {
			return Manifest{}, fmt.Errorf("pluginpkg: %w: %w", ErrBadArchive, err)
		}
		defer gz.Close() //nolint:errcheck // read-only, extraction already reported its own errors
		r = gz
	}

	if err := os.RemoveAll(destDir); err != nil {
		return Manifest{}, fmt.Errorf("pluginpkg: clear %q: %w", destDir, err)
	}
	if err := os.MkdirAll(destDir, 0o700); err != nil {
		return Manifest{}, fmt.Errorf("pluginpkg: create %q: %w", destDir, err)
	}

	if err := extractEntries(tar.NewReader(r), destDir); err != nil {
		return Manifest{}, err
	}

	manifest, err := readManifest(destDir)
	if err != nil {
		return Manifest{}, err
	}
	entrypoint := filepath.Join(destDir, manifest.Entrypoint)
	info, err := os.Stat(entrypoint)
	if err != nil {
		return Manifest{}, fmt.Errorf("pluginpkg: %q: %w", manifest.Entrypoint, ErrEntrypointMissing)
	}
	// A tar built without the exec bit set should still run.
	if err := os.Chmod(entrypoint, info.Mode()|0o111); err != nil {
		return Manifest{}, fmt.Errorf("pluginpkg: chmod %q: %w", entrypoint, err)
	}
	return manifest, nil
}

// extractEntries writes every regular file and directory entry from tr into
// destDir. Symlinks, hardlinks, and device entries are skipped.
func extractEntries(tr *tar.Reader, destDir string) error {
	for {
		hdr, err := tr.Next()
		if errors.Is(err, io.EOF) {
			return nil
		}
		if err != nil {
			return fmt.Errorf("pluginpkg: %w: %w", ErrBadArchive, err)
		}

		switch hdr.Typeflag {
		case tar.TypeDir:
			path, err := safeJoin(destDir, hdr.Name)
			if err != nil {
				return err
			}
			if err := os.MkdirAll(path, 0o700); err != nil {
				return fmt.Errorf("pluginpkg: create %q: %w", path, err)
			}
		case tar.TypeReg:
			path, err := safeJoin(destDir, hdr.Name)
			if err != nil {
				return err
			}
			if err := writeEntry(path, tr, hdr.FileInfo().Mode()); err != nil {
				return err
			}
		default:
			// A symlink target is exactly the kind of entry that could
			// otherwise escape destDir.
			continue
		}
	}
}

// safeJoin joins base and rel, and rejects a rel that is absolute or
// resolves outside base.
func safeJoin(base, rel string) (string, error) {
	if rel == "" || filepath.IsAbs(rel) {
		return "", fmt.Errorf("pluginpkg: %q: %w", rel, ErrUnsafePath)
	}
	joined := filepath.Join(base, rel)
	up := ".." + string(filepath.Separator)
	if r, err := filepath.Rel(base, joined); err != nil || r == ".." || strings.HasPrefix(r, up) {
		return "", fmt.Errorf("pluginpkg: %q: %w", rel, ErrUnsafePath)
	}
	return joined, nil
}

// writeEntry writes r to path with mode, creating parent directories as
// needed.
func writeEntry(path string, r io.Reader, mode os.FileMode) (err error) {
	if mkdirErr := os.MkdirAll(filepath.Dir(path), 0o700); mkdirErr != nil {
		return fmt.Errorf("pluginpkg: create %q: %w", filepath.Dir(path), mkdirErr)
	}
	out, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, mode) //nolint:gosec // path already validated by safeJoin
	if err != nil {
		return fmt.Errorf("pluginpkg: create %q: %w", path, err)
	}
	defer func() {
		// A Close failure here can still mean lost, unflushed data.
		if closeErr := out.Close(); err == nil && closeErr != nil {
			err = fmt.Errorf("pluginpkg: close %q: %w", path, closeErr)
		}
	}()

	if _, err = io.Copy(out, r); err != nil {
		return fmt.Errorf("pluginpkg: write %q: %w", path, err)
	}
	return nil
}

// readManifest reads and validates dir's manifest.json.
func readManifest(dir string) (Manifest, error) {
	data, err := os.ReadFile(filepath.Join(dir, ManifestFile)) //nolint:gosec // dir is a kevin-managed workspace path, not user input
	if errors.Is(err, os.ErrNotExist) {
		return Manifest{}, ErrManifestMissing
	}
	if err != nil {
		return Manifest{}, fmt.Errorf("pluginpkg: read manifest: %w", err)
	}

	var m Manifest
	if err := json.Unmarshal(data, &m); err != nil {
		return Manifest{}, fmt.Errorf("pluginpkg: %w: %w", ErrManifestInvalid, err)
	}
	if m.ManifestVersion != CurrentManifestVersion {
		return Manifest{}, fmt.Errorf("pluginpkg: got %d, want %d: %w", m.ManifestVersion, CurrentManifestVersion, ErrManifestVersion)
	}
	if m.Name == "" || m.Version == "" {
		return Manifest{}, fmt.Errorf("pluginpkg: manifest name and version are required: %w", ErrManifestInvalid)
	}
	if _, err := safeJoin(dir, m.Entrypoint); err != nil {
		return Manifest{}, fmt.Errorf("pluginpkg: manifest entrypoint: %w", ErrManifestInvalid)
	}
	return m, nil
}

func resultFrom(m Manifest, destDir string) Result {
	return Result{
		Name:    m.Name,
		Version: m.Version,
		Cmd:     filepath.Join(destDir, m.Entrypoint),
		Args:    m.Args,
	}
}
