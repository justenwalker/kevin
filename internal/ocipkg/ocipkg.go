// Package ocipkg fetches a kevin plugin package from an OCI registry into a
// local, content-addressed cache, ready for internal/pluginpkg.Extract, and
// pushes one built by internal/pluginpkg.Pack to a registry.
package ocipkg

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"

	"cuelabs.dev/go/oci/ociregistry"
	"cuelabs.dev/go/oci/ociregistry/ociauth"
	"cuelabs.dev/go/oci/ociregistry/ociclient"
	"cuelabs.dev/go/oci/ociregistry/ociref"
	digest "github.com/opencontainers/go-digest"
	specs "github.com/opencontainers/image-spec/specs-go"
	ocispec "github.com/opencontainers/image-spec/specs-go/v1"

	"github.com/justenwalker/kevin/internal/pkgcache"
)

// MediaType identifies an OCI manifest layer as a kevin plugin package - the
// same tar (optionally gzip) format internal/pluginpkg.Extract parses.
const MediaType = "application/vnd.kevin.plugin.package.v1.tar"

// SignatureMediaType identifies an OCI manifest layer as a detached
// minisign signature (a .minisig file) over a kevin plugin package's tar
// bytes.
const SignatureMediaType = "application/vnd.kevin.plugin.signature.v1+minisign"

// dockerManifestListMediaType is the legacy Docker equivalent of
// ocispec.MediaTypeImageIndex - some publishing tools still emit it.
const dockerManifestListMediaType = "application/vnd.docker.distribution.manifest.list.v2+json"

// Fetch resolves ref (e.g. "ghcr.io/acme/plugin:v1" or
// "ghcr.io/acme/plugin@sha256:...") and returns the local path of its cached
// plugin-package blob and its digest ("sha256:<hex>") - pass both straight
// into pluginpkg.Extract unmodified.
func Fetch(ctx context.Context, ref string) (string, string, error) {
	r, err := ociref.Parse(ref)
	if err != nil {
		return "", "", fmt.Errorf("ocipkg: %q: %w: %w", ref, ErrBadReference, err)
	}
	if r.Tag == "" && r.Digest == "" {
		return "", "", fmt.Errorf("ocipkg: %q: %w: needs a tag or a digest", ref, ErrBadReference)
	}

	reg, err := newRegistry(r.Host, ErrFetch)
	if err != nil {
		return "", "", err
	}
	return fetch(ctx, reg, r)
}

// Push builds a single-layer OCI manifest around tarPath's content - the
// same tar (optionally gzip) format pluginpkg.Extract expects - and pushes
// it to ref (e.g. "ghcr.io/acme/plugin:v1"), tagged. It returns the pushed
// manifest's digest ("sha256:<hex>").
func Push(ctx context.Context, ref, tarPath string) (string, error) {
	r, err := ociref.Parse(ref)
	if err != nil {
		return "", fmt.Errorf("ocipkg: %q: %w: %w", ref, ErrBadReference, err)
	}
	if r.Tag == "" {
		return "", fmt.Errorf("ocipkg: %q: %w: needs a tag", ref, ErrBadReference)
	}

	reg, err := newRegistry(r.Host, ErrPush)
	if err != nil {
		return "", err
	}
	return push(ctx, reg, r, tarPath)
}

// push does the hash/push-blob/push-manifest work against reg.
func push(ctx context.Context, reg ociregistry.Interface, r ociref.Reference, tarPath string) (string, error) {
	layer, err := pushBlobLayer(ctx, reg, r.Repository, tarPath, MediaType)
	if err != nil {
		return "", err
	}
	return pushArtifactManifest(ctx, reg, r.Repository, r.Tag, layer, MediaType)
}

// pushArtifactManifest pushes a single-layer OCI artifact manifest - no real
// config, the spec's own empty-config convention, since a kevin plugin
// package or signature has nothing to put there - tagged tag, and returns
// the pushed manifest's digest.
func pushArtifactManifest(ctx context.Context, reg ociregistry.Interface, repo, tag string, layer ocispec.Descriptor, artifactType string) (string, error) {
	if _, err := reg.PushBlob(ctx, repo, ocispec.DescriptorEmptyJSON, bytes.NewReader(ocispec.DescriptorEmptyJSON.Data)); err != nil {
		return "", fmt.Errorf("ocipkg: push config: %w: %w", ErrPush, err)
	}

	m := ocispec.Manifest{
		Versioned:    specs.Versioned{SchemaVersion: 2},
		MediaType:    ocispec.MediaTypeImageManifest,
		ArtifactType: artifactType,
		Config:       ocispec.DescriptorEmptyJSON,
		Layers:       []ocispec.Descriptor{layer},
	}
	data, err := json.Marshal(m)
	if err != nil {
		return "", fmt.Errorf("ocipkg: encode manifest: %w", err)
	}
	desc, err := reg.PushManifest(ctx, repo, tag, data, ocispec.MediaTypeImageManifest)
	if err != nil {
		return "", fmt.Errorf("ocipkg: push manifest: %w: %w", ErrPush, err)
	}
	return desc.Digest.String(), nil
}

// pushBlobLayer pushes path's content as a layer blob with mediaType and
// returns its descriptor.
func pushBlobLayer(ctx context.Context, reg ociregistry.Interface, repo, path, mediaType string) (ocispec.Descriptor, error) {
	f, err := os.Open(path) //nolint:gosec // path names a caller-chosen artifact to publish, the whole point
	if err != nil {
		return ocispec.Descriptor{}, fmt.Errorf("ocipkg: open %q: %w", path, err)
	}
	defer f.Close() //nolint:errcheck // read-only handle, nothing to flush

	h := sha256.New()
	size, err := io.Copy(h, f)
	if err != nil {
		return ocispec.Descriptor{}, fmt.Errorf("ocipkg: hash %q: %w", path, err)
	}
	if _, err := f.Seek(0, io.SeekStart); err != nil {
		return ocispec.Descriptor{}, fmt.Errorf("ocipkg: seek %q: %w", path, err)
	}

	desc := ocispec.Descriptor{
		MediaType: mediaType,
		Digest:    digest.NewDigestFromBytes(digest.SHA256, h.Sum(nil)),
		Size:      size,
	}
	bw, err := reg.PushBlobChunked(ctx, repo, 0)
	if err != nil {
		return ocispec.Descriptor{}, fmt.Errorf("ocipkg: push layer: %w: %w", ErrPush, err)
	}
	defer bw.Cancel() //nolint:errcheck // no-op once committed, best-effort otherwise
	if _, err := io.Copy(bw, f); err != nil {
		return ocispec.Descriptor{}, fmt.Errorf("ocipkg: push layer: %w: %w", ErrPush, err)
	}
	if _, err := bw.Commit(desc.Digest); err != nil {
		return ocispec.Descriptor{}, fmt.Errorf("ocipkg: push layer: %w: %w", ErrPush, err)
	}
	return desc, nil
}

// FetchSignature fetches the detached minisign signature published
// alongside ref's package (pkgDigest, as returned by Fetch), tagged by the
// cosign-style fallback convention ("sha256-<digest>.sig"). The OCI client
// kevin uses has no referrers-API support, so this reuses the same
// ResolveTag/GetManifest/GetBlob calls Fetch already makes for the package
// itself. The result is never cached: a detached signature is tiny.
func FetchSignature(ctx context.Context, ref, pkgDigest string) ([]byte, error) {
	r, err := ociref.Parse(ref)
	if err != nil {
		return nil, fmt.Errorf("ocipkg: %q: %w: %w", ref, ErrBadReference, err)
	}
	dig, err := digest.Parse(pkgDigest)
	if err != nil {
		return nil, fmt.Errorf("ocipkg: %q: %w: %w", pkgDigest, ErrBadReference, err)
	}

	reg, err := newRegistry(r.Host, ErrFetch)
	if err != nil {
		return nil, err
	}
	return fetchSignature(ctx, reg, r, dig)
}

// fetchSignature does the resolve-tag/fetch-manifest/fetch-blob work
// against reg.
func fetchSignature(ctx context.Context, reg ociregistry.Interface, r ociref.Reference, pkgDigest digest.Digest) ([]byte, error) {
	tag := signatureTag(pkgDigest)
	desc, err := reg.ResolveTag(ctx, r.Repository, tag)
	if err != nil {
		return nil, fmt.Errorf("ocipkg: resolve %s:%s: %w: %w", r.Repository, tag, ErrFetch, err)
	}
	manifest, err := fetchManifest(ctx, reg, r.Repository, desc.Digest)
	if err != nil {
		return nil, err
	}
	layer, err := signatureLayer(manifest)
	if err != nil {
		return nil, err
	}
	br, err := reg.GetBlob(ctx, r.Repository, layer.Digest)
	if err != nil {
		return nil, fmt.Errorf("ocipkg: fetch blob %s: %w: %w", layer.Digest, ErrFetch, err)
	}
	defer br.Close() //nolint:errcheck // read-only, the read error below is what's reported

	data, err := io.ReadAll(br) // a detached signature is a few hundred bytes
	if err != nil {
		return nil, fmt.Errorf("ocipkg: read blob %s: %w: %w", layer.Digest, ErrFetch, err)
	}
	return data, nil
}

// signatureTag derives the cosign-style fallback tag that carries
// pkgDigest's detached signature.
func signatureTag(pkgDigest digest.Digest) string {
	return "sha256-" + pkgDigest.Encoded() + ".sig"
}

// signatureLayer picks manifest's single detached-signature layer.
func signatureLayer(m ocispec.Manifest) (ocispec.Descriptor, error) {
	if len(m.Layers) == 0 || m.Layers[0].MediaType != SignatureMediaType {
		got := "no layers"
		if len(m.Layers) > 0 {
			got = m.Layers[0].MediaType
		}
		return ocispec.Descriptor{}, fmt.Errorf("ocipkg: got %q, want %q: %w", got, SignatureMediaType, ErrMediaType)
	}
	return m.Layers[0], nil
}

// PushSignature publishes sigPath (a detached minisign .minisig file) as
// ref's package signature, tagged by the cosign-style fallback convention
// ("sha256-<digest>.sig"), resolving ref's own package tag or digest
// first. It returns the pushed signature manifest's digest.
func PushSignature(ctx context.Context, ref, sigPath string) (string, error) {
	r, err := ociref.Parse(ref)
	if err != nil {
		return "", fmt.Errorf("ocipkg: %q: %w: %w", ref, ErrBadReference, err)
	}
	reg, err := newRegistry(r.Host, ErrPush)
	if err != nil {
		return "", err
	}
	return pushSignature(ctx, reg, r, sigPath)
}

// pushSignature does the resolve/hash/push-blob/push-manifest work against
// reg.
func pushSignature(ctx context.Context, reg ociregistry.Interface, r ociref.Reference, sigPath string) (string, error) {
	// The fallback tag must carry the same digest Fetch resolves and
	// reports - the package layer's digest, not the manifest's - so
	// resolve the package exactly as Fetch does.
	manifest, err := resolveManifest(ctx, reg, r)
	if err != nil {
		return "", fmt.Errorf("%w: %w", ErrPush, err)
	}
	pkgLayer, err := packageLayer(manifest)
	if err != nil {
		return "", err
	}

	layer, err := pushBlobLayer(ctx, reg, r.Repository, sigPath, SignatureMediaType)
	if err != nil {
		return "", err
	}
	return pushArtifactManifest(ctx, reg, r.Repository, signatureTag(pkgLayer.Digest), layer, SignatureMediaType)
}

// newRegistry builds an authenticated client for host, reusing whatever
// credentials docker login (or a credential helper) already wrote. A
// failure is wrapped in sentinel, so a caller's errors.Is check matches the
// operation it attempted (Fetch's ErrFetch, or Push's ErrPush) even when the
// failure happened before any request reached the registry.
func newRegistry(host string, sentinel error) (ociregistry.Interface, error) { //nolint:ireturn // ociclient.New itself returns this interface; there is no concrete type to return instead
	cfg, err := ociauth.Load(nil)
	if err != nil {
		return nil, fmt.Errorf("ocipkg: load registry credentials: %w: %w", sentinel, err)
	}
	transport := ociauth.NewStdTransport(ociauth.StdTransportParams{Config: cfg})
	reg, err := ociclient.New(host, &ociclient.Options{Transport: transport})
	if err != nil {
		return nil, fmt.Errorf("ocipkg: %q: %w: %w", host, sentinel, err)
	}
	return reg, nil
}

// fetch does the resolve/select/cache work against reg.
func fetch(ctx context.Context, reg ociregistry.Interface, r ociref.Reference) (string, string, error) {
	manifest, err := resolveManifest(ctx, reg, r)
	if err != nil {
		return "", "", err
	}
	layer, err := packageLayer(manifest)
	if err != nil {
		return "", "", err
	}
	if layer.Digest.Algorithm() != digest.SHA256 {
		return "", "", fmt.Errorf("ocipkg: %s: %w: pluginpkg only understands sha256", layer.Digest, ErrMediaType)
	}

	cachePath := pkgcache.Path(layer.Digest.Encoded())
	// Content-addressed and immutable: an existing file at this path was
	// already verified the first time it was written. No separate
	// freshness check is needed, unlike pluginpkg's mtime marker.
	if _, statErr := os.Stat(cachePath); statErr == nil {
		return cachePath, layer.Digest.String(), nil
	}
	if err := cacheBlob(ctx, reg, r.Repository, layer, cachePath); err != nil {
		return "", "", err
	}
	return cachePath, layer.Digest.String(), nil
}

// resolveManifest fetches r's manifest (by digest if pinned, else by tag),
// following one level of multi-arch image index if present.
func resolveManifest(ctx context.Context, reg ociregistry.Interface, r ociref.Reference) (ocispec.Manifest, error) {
	var desc ociregistry.Descriptor
	var err error
	if r.Digest != "" {
		desc, err = reg.ResolveManifest(ctx, r.Repository, r.Digest)
	} else {
		desc, err = reg.ResolveTag(ctx, r.Repository, r.Tag)
	}
	if err != nil {
		return ocispec.Manifest{}, fmt.Errorf("ocipkg: resolve %s: %w: %w", r, ErrFetch, err)
	}
	return fetchManifest(ctx, reg, r.Repository, desc.Digest)
}

// fetchManifest fetches the manifest content named by digest. When it is a
// multi-arch index, fetchManifest resolves the entry for this host's
// platform and recurses once into it.
func fetchManifest(ctx context.Context, reg ociregistry.Interface, repo string, dig digest.Digest) (ocispec.Manifest, error) {
	br, err := reg.GetManifest(ctx, repo, dig)
	if err != nil {
		return ocispec.Manifest{}, fmt.Errorf("ocipkg: fetch manifest %s: %w: %w", dig, ErrFetch, err)
	}
	defer br.Close()            //nolint:errcheck // read-only, extraction already reported its own errors
	data, err := io.ReadAll(br) // a manifest or index is a few KB; unlike the layer blob, buffering it is fine
	if err != nil {
		return ocispec.Manifest{}, fmt.Errorf("ocipkg: read manifest %s: %w: %w", dig, ErrFetch, err)
	}

	var probe struct {
		MediaType string `json:"mediaType"` //nolint:tagliatelle // the OCI spec's own field name, not kevin's
	}
	if err := json.Unmarshal(data, &probe); err != nil {
		return ocispec.Manifest{}, fmt.Errorf("ocipkg: decode manifest %s: %w: %w", dig, ErrMediaType, err)
	}

	if probe.MediaType == ocispec.MediaTypeImageIndex || probe.MediaType == dockerManifestListMediaType {
		var idx ocispec.Index
		if err := json.Unmarshal(data, &idx); err != nil {
			return ocispec.Manifest{}, fmt.Errorf("ocipkg: decode index %s: %w: %w", dig, ErrMediaType, err)
		}
		chosen, err := selectPlatform(idx.Manifests)
		if err != nil {
			return ocispec.Manifest{}, err
		}
		return fetchManifest(ctx, reg, repo, chosen.Digest)
	}

	var m ocispec.Manifest
	if err := json.Unmarshal(data, &m); err != nil {
		return ocispec.Manifest{}, fmt.Errorf("ocipkg: decode manifest %s: %w: %w", dig, ErrMediaType, err)
	}
	return m, nil
}

// selectPlatform picks the index entry matching this host's OS and
// architecture. Variant is not checked: Go's runtime does not expose the
// ARM variant (e.g. GOARM) a binary was built with, so kevin cannot match
// it precisely either way.
//
// ponytail: OS+Arch only, no variant check; tighten if a wrong-variant
// binary actually ships to someone.
func selectPlatform(candidates []ocispec.Descriptor) (ocispec.Descriptor, error) {
	for _, d := range candidates {
		if d.Platform != nil && d.Platform.OS == runtime.GOOS && d.Platform.Architecture == runtime.GOARCH {
			return d, nil
		}
	}

	available := make([]string, 0, len(candidates))
	for _, d := range candidates {
		if d.Platform == nil {
			continue
		}
		s := d.Platform.OS + "/" + d.Platform.Architecture
		if d.Platform.Variant != "" {
			s += "/" + d.Platform.Variant
		}
		available = append(available, s)
	}
	sort.Strings(available)
	return ocispec.Descriptor{}, fmt.Errorf("ocipkg: want %s/%s, available: %s: %w",
		runtime.GOOS, runtime.GOARCH, strings.Join(available, ", "), ErrNoMatchingPlatform)
}

// packageLayer picks manifest's single kevin plugin package layer.
func packageLayer(m ocispec.Manifest) (ocispec.Descriptor, error) {
	if len(m.Layers) == 0 || m.Layers[0].MediaType != MediaType {
		got := "no layers"
		if len(m.Layers) > 0 {
			got = m.Layers[0].MediaType
		}
		return ocispec.Descriptor{}, fmt.Errorf("ocipkg: got %q, want %q: %w", got, MediaType, ErrMediaType)
	}
	return m.Layers[0], nil
}

// cacheBlob streams layer's content to cachePath, atomically, only after its
// bytes hash to layer.Digest - a corrupt or tampered download never reaches
// the cache under the digest's name.
func cacheBlob(ctx context.Context, reg ociregistry.Interface, repo string, layer ocispec.Descriptor, cachePath string) error {
	br, err := reg.GetBlob(ctx, repo, layer.Digest)
	if err != nil {
		return fmt.Errorf("ocipkg: fetch blob %s: %w: %w", layer.Digest, ErrFetch, err)
	}
	defer br.Close() //nolint:errcheck // read-only, extraction already reported its own errors

	dir := filepath.Dir(cachePath)
	if mkdirErr := os.MkdirAll(dir, 0o700); mkdirErr != nil {
		return fmt.Errorf("ocipkg: create %q: %w", dir, mkdirErr)
	}
	tmp, err := os.CreateTemp(dir, "*.tmp") // same filesystem as cachePath, for an atomic rename
	if err != nil {
		return fmt.Errorf("ocipkg: create temp file: %w", err)
	}
	defer os.Remove(tmp.Name()) //nolint:errcheck // no-op once renamed away

	h := sha256.New()
	if _, err := io.Copy(io.MultiWriter(tmp, h), br); err != nil {
		tmp.Close() //nolint:errcheck,gosec // best effort; the copy error below is what's reported
		return fmt.Errorf("ocipkg: download %s: %w", layer.Digest, err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("ocipkg: close temp file: %w", err)
	}

	got := digest.NewDigestFromBytes(digest.SHA256, h.Sum(nil))
	if got.String() != layer.Digest.String() {
		return fmt.Errorf("ocipkg: %s: got %s: %w", layer.Digest, got, ErrBlobMismatch)
	}
	if err := os.Rename(tmp.Name(), cachePath); err != nil {
		return fmt.Errorf("ocipkg: %w", err)
	}
	return nil
}
