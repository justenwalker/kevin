package pluginindex

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"cuelang.org/go/cue"
	"github.com/jedisct1/go-minisign"
	"golang.org/x/mod/semver"

	"github.com/justenwalker/kevin/internal/config"
)

// Plugin is one plugins/<name> directory of an index repo: its identity
// (plugin.yaml) plus every release found under versions/.
type Plugin struct {
	Name       string
	Summary    string
	Homepage   string
	Maintainer string

	// Signers lists the plugin's acceptable release signers, from
	// plugin.yaml. "kevin plugin index install" trusts every entry whose
	// Scheme matches the version being installed.
	Signers []Signer

	// VersionSource, when set, names a separate git repo holding this
	// plugin's own plugins/<name>/versions/ tree, instead of the index
	// repo's own. See [Signer] and syncVersionSource in update.go.
	VersionSource string

	// Repo is the index source this plugin was loaded from. The loader
	// that walks a repo's plugins/ tree fills this in - loadPlugin itself
	// only sees one plugin directory, never its owning Source.
	Repo Source

	// Versions is sorted descending by semver.
	Versions []Version
}

// Signer is one acceptable release signer for a Plugin (see
// #Signer in schema.cue).
type Signer struct {
	Scheme config.SigningScheme

	// Key is the signer's raw minisign public key text, set when Scheme
	// is [config.SigningSchemeMinisign].
	Key string

	// Identity and Issuer are set when Scheme is
	// [config.SigningSchemeSigstore].
	Identity string
	Issuer   string
}

// Version is one release of a Plugin (versions/<semver>.yaml).
type Version struct {
	Version string
	Source  config.PluginSpec

	value cue.Value
}

// SignersFor reports every entry in p.Signers whose Scheme matches
// scheme.
func (p Plugin) SignersFor(scheme config.SigningScheme) []Signer {
	var out []Signer
	for _, s := range p.Signers {
		if s.Scheme == scheme {
			out = append(out, s)
		}
	}
	return out
}

// Latest reports the highest non-prerelease [Version], or the highest
// prerelease if that is all there is. It reports false when Versions is
// empty.
func (p Plugin) Latest() (Version, bool) {
	if len(p.Versions) == 0 {
		return Version{}, false
	}
	for _, v := range p.Versions {
		if semver.Prerelease(semverKey(v.Version)) == "" {
			return v, true
		}
	}
	return p.Versions[0], true
}

// semverKey adapts a bare "X.Y.Z[-pre]" version string (this format's own
// shape, enforced by #Version's regex) to the "vX.Y.Z[-pre]" form
// golang.org/x/mod/semver requires.
func semverKey(v string) string {
	return "v" + v
}

// loadPlugin reads and validates one plugins/<name> directory: plugin.yaml
// against #PluginMeta, and every versions/*.yaml against #Version. It
// returns an error for a malformed plugin.yaml, a malformed version file,
// or a version file whose "version" field does not match its own filename
// stem. A directory with plugin.yaml but no versions/*.yaml yet is not an
// error - it loads with an empty Versions. Versions load from dir's own
// versions/ subdirectory, with no signature requirement - the caller
// that knows about a plugin's VersionSource (see update.go) loads
// versions separately, from wherever that points.
func loadPlugin(dir string) (Plugin, error) {
	p, err := loadPluginMeta(dir)
	if err != nil {
		return Plugin{}, err
	}
	p.Versions, err = loadVersions(context.Background(), filepath.Join(dir, "versions"), nil)
	if err != nil {
		return Plugin{}, err
	}
	return p, nil
}

// loadPluginMeta reads and validates dir's plugin.yaml against
// #PluginMeta. Versions is left empty - the caller loads it separately
// (see loadPlugin and update.go's per-plugin VersionSource handling).
func loadPluginMeta(dir string) (Plugin, error) {
	metaPath := filepath.Join(dir, "plugin.yaml")
	metaSrc, err := os.ReadFile(metaPath) //nolint:gosec // dir is a locally cloned index repo path, not user input
	if err != nil {
		return Plugin{}, fmt.Errorf("pluginindex: read %q: %w", metaPath, err)
	}
	metaValue, err := validateAgainst(cue.ParsePath("#PluginMeta"), metaPath, metaSrc)
	if err != nil {
		return Plugin{}, err
	}

	var meta struct {
		Name          string `json:"name"`
		Summary       string `json:"summary"`
		Homepage      string `json:"homepage"`
		Maintainer    string `json:"maintainer"`
		VersionSource string `json:"version_source"`
		Signers       []struct {
			Scheme   config.SigningScheme `json:"scheme"`
			Key      string               `json:"key"`
			Identity string               `json:"identity"`
			Issuer   string               `json:"issuer"`
		} `json:"signers"`
	}
	if decodeErr := decodeValue(metaValue, &meta); decodeErr != nil {
		return Plugin{}, fmt.Errorf("pluginindex: decode %q: %w", metaPath, decodeErr)
	}

	signers := make([]Signer, len(meta.Signers))
	for i, s := range meta.Signers {
		if s.Scheme == config.SigningSchemeMinisign {
			if _, keyErr := minisign.DecodePublicKey(s.Key); keyErr != nil {
				return Plugin{}, fmt.Errorf("pluginindex: %q: signer %d: %w: %w", metaPath, i, ErrBadSigner, keyErr)
			}
		}
		signers[i] = Signer{Scheme: s.Scheme, Key: s.Key, Identity: s.Identity, Issuer: s.Issuer}
	}

	return Plugin{
		Name:          meta.Name,
		Summary:       meta.Summary,
		Homepage:      meta.Homepage,
		Maintainer:    meta.Maintainer,
		Signers:       signers,
		VersionSource: meta.VersionSource,
	}, nil
}

// loadVersions reads and validates every versions/*.yaml file in dir,
// sorted descending by semver. A dir that does not exist yet reports no
// versions, not an error.
//
// signers gates a federated version source (see update.go's
// syncVersionSource): when non-nil, every version file must carry a
// detached signature - a "<file>.minisig" sibling verified against
// signers' own minisign entries, or a "<file>.sigstore.json" sibling
// verified against signers' own sigstore identities - checked only
// against signers, never the global trust store, since a federated
// source is trusted only as far as this plugin's own declared signers
// allow. signers nil (the default, same-repo case) requires no
// signature at all. ctx is only used for the sigstore path's
// "cosign verify-blob" call.
func loadVersions(ctx context.Context, dir string, signers []Signer) ([]Version, error) {
	entries, err := os.ReadDir(dir)
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("pluginindex: read %q: %w", dir, err)
	}

	versions := make([]Version, 0, len(entries))
	for _, e := range entries {
		if e.IsDir() || filepath.Ext(e.Name()) != ".yaml" {
			continue
		}
		path := filepath.Join(dir, e.Name())
		src, err := os.ReadFile(path) //nolint:gosec // dir is a locally cloned index repo path, not user input
		if err != nil {
			return nil, fmt.Errorf("pluginindex: read %q: %w", path, err)
		}
		if signers != nil {
			if verifyErr := verifyVersionFile(ctx, path, signers); verifyErr != nil {
				return nil, verifyErr
			}
		}
		value, err := validateAgainst(cue.ParsePath("#Version"), path, src)
		if err != nil {
			return nil, err
		}

		var decoded struct {
			Version string            `json:"version"`
			Source  config.PluginSpec `json:"source"`
		}
		if err := decodeValue(value, &decoded); err != nil {
			return nil, fmt.Errorf("pluginindex: decode %q: %w", path, err)
		}

		stem := strings.TrimSuffix(e.Name(), ".yaml")
		if decoded.Version != stem {
			return nil, fmt.Errorf("pluginindex: %q: version %q does not match filename %w",
				path, decoded.Version, ErrVersionMismatch)
		}

		versions = append(versions, Version{Version: decoded.Version, Source: decoded.Source, value: value})
	}

	sort.Slice(versions, func(i, j int) bool {
		return semver.Compare(semverKey(versions[i].Version), semverKey(versions[j].Version)) > 0
	})
	return versions, nil
}

// decodeValue renders v as JSON, then unmarshals it into into.
func decodeValue(v cue.Value, into any) error {
	data, err := v.MarshalJSON()
	if err != nil {
		return fmt.Errorf("pluginindex: marshal: %w", err)
	}
	if err := json.Unmarshal(data, into); err != nil {
		return fmt.Errorf("pluginindex: unmarshal: %w", err)
	}
	return nil
}
