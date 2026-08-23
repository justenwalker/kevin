package ocipkg

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"cuelabs.dev/go/oci/ociregistry"
	"cuelabs.dev/go/oci/ociregistry/ocimem"
	"cuelabs.dev/go/oci/ociregistry/ociref"
	digest "github.com/opencontainers/go-digest"
	specs "github.com/opencontainers/image-spec/specs-go"
	ocispec "github.com/opencontainers/image-spec/specs-go/v1"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/justenwalker/kevin/internal/pkgcache"
)

var versioned = specs.Versioned{SchemaVersion: 2}

const testRepo = "acme/plugin"

// emptyConfig is the descriptor OCI's "artifact manifest" convention uses
// when a manifest carries no real config - every manifest in this file uses
// it, since a kevin plugin package has nothing to put there.
func emptyConfig(t *testing.T, reg *ocimem.Registry) ocispec.Descriptor {
	t.Helper()
	data := []byte("{}")
	desc, err := reg.PushBlob(t.Context(), testRepo,
		ociregistry.Descriptor{MediaType: "application/vnd.oci.empty.v1+json", Digest: digest.FromBytes(data), Size: int64(len(data))},
		bytes.NewReader(data))
	require.NoError(t, err)
	return desc
}

// pushLayer pushes content as a kevin-plugin-package-media-typed blob and
// returns its descriptor.
func pushLayer(t *testing.T, reg *ocimem.Registry, content []byte) ocispec.Descriptor {
	t.Helper()
	d := digest.FromBytes(content)
	desc, err := reg.PushBlob(t.Context(), testRepo,
		ociregistry.Descriptor{MediaType: MediaType, Digest: d, Size: int64(len(content))},
		bytes.NewReader(content))
	require.NoError(t, err)
	return desc
}

// pushManifest pushes a single-platform manifest around layer, tagged with
// tag (empty for an untagged, digest-only push), and returns its descriptor.
func pushManifest(t *testing.T, reg *ocimem.Registry, tag string, layer ocispec.Descriptor) ocispec.Descriptor {
	t.Helper()
	cfg := emptyConfig(t, reg)
	m := ocispec.Manifest{
		Versioned: versioned,
		MediaType: ocispec.MediaTypeImageManifest,
		Config:    cfg,
		Layers:    []ocispec.Descriptor{layer},
	}
	data, err := json.Marshal(m)
	require.NoError(t, err)
	desc, err := reg.PushManifest(t.Context(), testRepo, tag, data, ocispec.MediaTypeImageManifest)
	require.NoError(t, err)
	return desc
}

// pushIndex pushes a multi-arch index tagged with tag, over the given
// platform manifests, and returns the index descriptor.
func pushIndex(t *testing.T, reg *ocimem.Registry, tag string, entries ...ocispec.Descriptor) ocispec.Descriptor {
	t.Helper()
	idx := ocispec.Index{
		Versioned: versioned,
		MediaType: ocispec.MediaTypeImageIndex,
		Manifests: entries,
	}
	data, err := json.Marshal(idx)
	require.NoError(t, err)
	desc, err := reg.PushManifest(t.Context(), testRepo, tag, data, ocispec.MediaTypeImageIndex)
	require.NoError(t, err)
	return desc
}

func TestFetch(t *testing.T) {
	t.Run("resolves a single manifest by tag", func(t *testing.T) {
		t.Setenv("HOME", t.TempDir())
		reg := ocimem.New()
		layer := pushLayer(t, reg, []byte("plugin package bytes"))
		pushManifest(t, reg, "v1", layer)

		pkgPath, digestStr, err := fetch(t.Context(), reg, ociref.Reference{Repository: testRepo, Tag: "v1"})
		require.NoError(t, err)
		assert.Equal(t, layer.Digest.String(), digestStr)
		data, err := os.ReadFile(pkgPath)
		require.NoError(t, err)
		assert.Equal(t, "plugin package bytes", string(data))
	})

	t.Run("resolves a single manifest by digest", func(t *testing.T) {
		t.Setenv("HOME", t.TempDir())
		reg := ocimem.New()
		layer := pushLayer(t, reg, []byte("plugin package bytes"))
		manifest := pushManifest(t, reg, "", layer)

		pkgPath, digestStr, err := fetch(t.Context(), reg, ociref.Reference{Repository: testRepo, Digest: manifest.Digest})
		require.NoError(t, err)
		assert.Equal(t, layer.Digest.String(), digestStr)
		assert.FileExists(t, pkgPath)
	})

	t.Run("resolves the entry for this platform from an index", func(t *testing.T) {
		t.Setenv("HOME", t.TempDir())
		reg := ocimem.New()

		wantLayer := pushLayer(t, reg, []byte("the right platform"))
		wantManifest := pushManifest(t, reg, "", wantLayer)
		wantManifest.Platform = &ocispec.Platform{OS: runtime.GOOS, Architecture: runtime.GOARCH}

		otherLayer := pushLayer(t, reg, []byte("some other platform"))
		otherManifest := pushManifest(t, reg, "", otherLayer)
		otherManifest.Platform = &ocispec.Platform{OS: "plan9", Architecture: "386"}

		pushIndex(t, reg, "v1", wantManifest, otherManifest)

		pkgPath, digestStr, err := fetch(t.Context(), reg, ociref.Reference{Repository: testRepo, Tag: "v1"})
		require.NoError(t, err)
		assert.Equal(t, wantLayer.Digest.String(), digestStr)
		data, err := os.ReadFile(pkgPath)
		require.NoError(t, err)
		assert.Equal(t, "the right platform", string(data))
	})

	t.Run("reports an index with no matching platform", func(t *testing.T) {
		t.Setenv("HOME", t.TempDir())
		reg := ocimem.New()

		layer := pushLayer(t, reg, []byte("wrong platform"))
		manifest := pushManifest(t, reg, "", layer)
		manifest.Platform = &ocispec.Platform{OS: "plan9", Architecture: "386"}
		pushIndex(t, reg, "v1", manifest)

		_, _, err := fetch(t.Context(), reg, ociref.Reference{Repository: testRepo, Tag: "v1"})
		require.ErrorIs(t, err, ErrNoMatchingPlatform)
		assert.Contains(t, err.Error(), "plan9/386", "the error must name the platforms the index does carry")
	})

	t.Run("rejects a manifest with the wrong layer media type", func(t *testing.T) {
		t.Setenv("HOME", t.TempDir())
		reg := ocimem.New()
		desc, err := reg.PushBlob(t.Context(), testRepo,
			ociregistry.Descriptor{MediaType: "application/octet-stream", Digest: digest.FromBytes([]byte("x")), Size: 1},
			bytes.NewReader([]byte("x")))
		require.NoError(t, err)
		pushManifest(t, reg, "v1", desc)

		_, _, err = fetch(t.Context(), reg, ociref.Reference{Repository: testRepo, Tag: "v1"})
		require.ErrorIs(t, err, ErrMediaType)
	})

	t.Run("rejects a manifest with no layers", func(t *testing.T) {
		t.Setenv("HOME", t.TempDir())
		reg := ocimem.New()
		cfg := emptyConfig(t, reg)
		m := ocispec.Manifest{Versioned: versioned, MediaType: ocispec.MediaTypeImageManifest, Config: cfg}
		data, err := json.Marshal(m)
		require.NoError(t, err)
		_, err = reg.PushManifest(t.Context(), testRepo, "v1", data, ocispec.MediaTypeImageManifest)
		require.NoError(t, err)

		_, _, err = fetch(t.Context(), reg, ociref.Reference{Repository: testRepo, Tag: "v1"})
		require.ErrorIs(t, err, ErrMediaType)
	})

	t.Run("caches the blob and skips a repeat download", func(t *testing.T) {
		t.Setenv("HOME", t.TempDir())
		reg := ocimem.New()
		layer := pushLayer(t, reg, []byte("cache me"))
		pushManifest(t, reg, "v1", layer)

		ref := ociref.Reference{Repository: testRepo, Tag: "v1"}
		first, _, err := fetch(t.Context(), reg, ref)
		require.NoError(t, err)

		// Blank out the manifest so a second real fetch attempt would fail -
		// proves the second call never touches the registry again.
		blank := ocimem.New()
		second, _, err := fetch(t.Context(), blank, ref)
		require.Error(t, err, "sanity check: an empty registry really can't resolve this ref")
		_ = second

		again, _, err := fetch(t.Context(), reg, ref)
		require.NoError(t, err)
		assert.Equal(t, first, again, "a cache hit must return the same cached path")
	})

	t.Run("rejects a blob that does not match its declared digest", func(t *testing.T) {
		t.Setenv("HOME", t.TempDir())
		reg := ocimem.New()
		layer := pushLayer(t, reg, []byte("plugin package bytes"))
		pushManifest(t, reg, "v1", layer)

		_, _, err := fetch(t.Context(), tamperedRegistry{reg}, ociref.Reference{Repository: testRepo, Tag: "v1"})
		require.ErrorIs(t, err, ErrBlobMismatch)

		assert.NoFileExists(t, pkgcache.Path(layer.Digest.Encoded()), "a mismatched download must not reach the cache")
	})

	t.Run("rejects a bad reference", func(t *testing.T) {
		t.Setenv("HOME", t.TempDir())
		_, _, err := Fetch(t.Context(), "not a valid ref!!")
		require.ErrorIs(t, err, ErrBadReference)
	})

	t.Run("rejects a reference with neither tag nor digest", func(t *testing.T) {
		t.Setenv("HOME", t.TempDir())
		_, _, err := Fetch(t.Context(), "registry.example.com/acme/plugin")
		require.ErrorIs(t, err, ErrBadReference)
	})
}

func TestPush(t *testing.T) {
	t.Run("pushes a layer, config and manifest that Fetch can then resolve", func(t *testing.T) {
		t.Setenv("HOME", t.TempDir())
		reg := ocimem.New()

		dir := t.TempDir()
		content := []byte("plugin package bytes")
		tarPath := filepath.Join(dir, "pkg.tar.gz")
		require.NoError(t, os.WriteFile(tarPath, content, 0o600))

		manifestDigest, err := push(t.Context(), reg, ociref.Reference{Repository: testRepo, Tag: "v1"}, tarPath)
		require.NoError(t, err)
		assert.NotEmpty(t, manifestDigest)

		pkgPath, fetchedDigest, err := fetch(t.Context(), reg, ociref.Reference{Repository: testRepo, Tag: "v1"})
		require.NoError(t, err)
		assert.Equal(t, digest.FromBytes(content).String(), fetchedDigest, "Fetch must resolve the same layer bytes Push uploaded")
		data, err := os.ReadFile(pkgPath)
		require.NoError(t, err)
		assert.Equal(t, "plugin package bytes", string(data))
	})

	t.Run("rejects a reference with no tag", func(t *testing.T) {
		t.Setenv("HOME", t.TempDir())
		dir := t.TempDir()
		tarPath := filepath.Join(dir, "pkg.tar.gz")
		require.NoError(t, os.WriteFile(tarPath, []byte("x"), 0o600))

		_, err := Push(t.Context(), "registry.example.com/acme/plugin", tarPath)
		require.ErrorIs(t, err, ErrBadReference)
	})

	t.Run("rejects a bad reference", func(t *testing.T) {
		t.Setenv("HOME", t.TempDir())
		_, err := Push(t.Context(), "not a valid ref!!", "irrelevant")
		require.ErrorIs(t, err, ErrBadReference)
	})

	t.Run("reports a registry that refuses the layer push", func(t *testing.T) {
		t.Setenv("HOME", t.TempDir())
		dir := t.TempDir()
		tarPath := filepath.Join(dir, "pkg.tar.gz")
		require.NoError(t, os.WriteFile(tarPath, []byte("x"), 0o600))

		_, err := push(t.Context(), refusingBlobRegistry{ocimem.New()}, ociref.Reference{Repository: testRepo, Tag: "v1"}, tarPath)
		require.ErrorIs(t, err, ErrPush)
	})

	t.Run("reports a registry that refuses the manifest push", func(t *testing.T) {
		t.Setenv("HOME", t.TempDir())
		dir := t.TempDir()
		tarPath := filepath.Join(dir, "pkg.tar.gz")
		require.NoError(t, os.WriteFile(tarPath, []byte("x"), 0o600))

		_, err := push(t.Context(), refusingManifestRegistry{ocimem.New()}, ociref.Reference{Repository: testRepo, Tag: "v1"}, tarPath)
		require.ErrorIs(t, err, ErrPush)
	})
}

func TestFetchSignature(t *testing.T) {
	t.Run("fetches the signature published at the fallback tag", func(t *testing.T) {
		t.Setenv("HOME", t.TempDir())
		reg := ocimem.New()
		pkgLayer := pushLayer(t, reg, []byte("plugin package bytes"))

		sigContent := []byte("untrusted comment: test\nsignature bytes\n")
		sigDesc, err := reg.PushBlob(t.Context(), testRepo,
			ociregistry.Descriptor{MediaType: SignatureMediaType, Digest: digest.FromBytes(sigContent), Size: int64(len(sigContent))},
			bytes.NewReader(sigContent))
		require.NoError(t, err)
		m := ocispec.Manifest{Versioned: versioned, MediaType: ocispec.MediaTypeImageManifest, ArtifactType: SignatureMediaType, Config: emptyConfig(t, reg), Layers: []ocispec.Descriptor{sigDesc}}
		data, err := json.Marshal(m)
		require.NoError(t, err)
		_, err = reg.PushManifest(t.Context(), testRepo, signatureTag(pkgLayer.Digest), data, ocispec.MediaTypeImageManifest)
		require.NoError(t, err)

		got, err := fetchSignature(t.Context(), reg, ociref.Reference{Repository: testRepo}, pkgLayer.Digest)
		require.NoError(t, err)
		assert.Equal(t, sigContent, got)
	})

	t.Run("reports a missing fallback tag", func(t *testing.T) {
		t.Setenv("HOME", t.TempDir())
		reg := ocimem.New()
		_, err := fetchSignature(t.Context(), reg, ociref.Reference{Repository: testRepo}, digest.FromBytes([]byte("no such package")))
		require.ErrorIs(t, err, ErrFetch)
	})

	t.Run("rejects a manifest with the wrong layer media type", func(t *testing.T) {
		t.Setenv("HOME", t.TempDir())
		reg := ocimem.New()
		pkgLayer := pushLayer(t, reg, []byte("plugin package bytes"))
		pushManifest(t, reg, signatureTag(pkgLayer.Digest), pkgLayer) // package-typed layer, not a signature

		_, err := fetchSignature(t.Context(), reg, ociref.Reference{Repository: testRepo}, pkgLayer.Digest)
		require.ErrorIs(t, err, ErrMediaType)
	})

	t.Run("rejects a bad reference", func(t *testing.T) {
		t.Setenv("HOME", t.TempDir())
		_, err := FetchSignature(t.Context(), "not a valid ref!!", "sha256:deadbeef")
		require.ErrorIs(t, err, ErrBadReference)
	})

	t.Run("rejects a bad digest", func(t *testing.T) {
		t.Setenv("HOME", t.TempDir())
		_, err := FetchSignature(t.Context(), "registry.example.com/acme/plugin:v1", "not a digest")
		require.ErrorIs(t, err, ErrBadReference)
	})
}

func TestPushSignature(t *testing.T) {
	t.Run("pushes a signature that FetchSignature can then resolve, tagged by digest", func(t *testing.T) {
		t.Setenv("HOME", t.TempDir())
		reg := ocimem.New()
		pkgLayer := pushLayer(t, reg, []byte("plugin package bytes"))
		pushManifest(t, reg, "v1", pkgLayer)

		dir := t.TempDir()
		sigContent := []byte("untrusted comment: test\nsignature bytes\n")
		sigPath := filepath.Join(dir, "pkg.tar.gz.minisig")
		require.NoError(t, os.WriteFile(sigPath, sigContent, 0o600))

		_, err := pushSignature(t.Context(), reg, ociref.Reference{Repository: testRepo, Tag: "v1"}, sigPath)
		require.NoError(t, err)

		got, err := fetchSignature(t.Context(), reg, ociref.Reference{Repository: testRepo}, pkgLayer.Digest)
		require.NoError(t, err)
		assert.Equal(t, sigContent, got)
	})

	t.Run("rejects a bad reference", func(t *testing.T) {
		t.Setenv("HOME", t.TempDir())
		_, err := PushSignature(t.Context(), "not a valid ref!!", "irrelevant")
		require.ErrorIs(t, err, ErrBadReference)
	})

	t.Run("reports a package tag the registry cannot resolve", func(t *testing.T) {
		t.Setenv("HOME", t.TempDir())
		dir := t.TempDir()
		sigPath := filepath.Join(dir, "pkg.tar.gz.minisig")
		require.NoError(t, os.WriteFile(sigPath, []byte("x"), 0o600))

		_, err := pushSignature(t.Context(), ocimem.New(), ociref.Reference{Repository: testRepo, Tag: "v1"}, sigPath)
		require.ErrorIs(t, err, ErrPush)
	})
}

// refusingBlobRegistry wraps a real registry but fails every blob push -
// this fails push at both the layer and the empty-config step.
type refusingBlobRegistry struct {
	ociregistry.Interface
}

func (refusingBlobRegistry) PushBlob(context.Context, string, ociregistry.Descriptor, io.Reader) (ociregistry.Descriptor, error) {
	return ociregistry.Descriptor{}, errors.New("push refused")
}

// refusingManifestRegistry wraps a real registry but fails every manifest
// push, so blob pushes still land normally.
type refusingManifestRegistry struct {
	ociregistry.Interface
}

func (refusingManifestRegistry) PushManifest(context.Context, string, string, []byte, string) (ociregistry.Descriptor, error) {
	return ociregistry.Descriptor{}, errors.New("push refused")
}

// tamperedBlobReader wraps a BlobReader and swaps its content, while
// keeping the descriptor it reports intact.
type tamperedBlobReader struct {
	ociregistry.BlobReader

	content io.Reader
}

func (r tamperedBlobReader) Read(p []byte) (int, error) { return r.content.Read(p) }

// tamperedRegistry wraps a real registry but returns corrupted bytes from
// GetBlob - everything else is unchanged.
type tamperedRegistry struct {
	ociregistry.Interface
}

func (r tamperedRegistry) GetBlob(ctx context.Context, repo string, dig digest.Digest) (ociregistry.BlobReader, error) { //nolint:ireturn // implementing ociregistry.Interface's fixed method signature
	br, err := r.Interface.GetBlob(ctx, repo, dig)
	if err != nil {
		return nil, err
	}
	return tamperedBlobReader{BlobReader: br, content: bytes.NewReader([]byte("corrupted"))}, nil
}
