package state

import (
	"os"
	"path/filepath"
)

// WorkspaceDir is the state directory of one project, relative to the project
// directory.
const WorkspaceDir = ".kevin"

// Environment variables that override the default state directories.
const (
	UserStateDirEnv    = "KEVIN_USER_STATE_DIR"
	ProjectStateDirEnv = "KEVIN_PROJECT_STATE_DIR"
)

// UserStateDir returns the directory path for storing user-specific state.
// It defaults to "$HOME/.kevin" if not set in the environment.
func UserStateDir() string {
	if v := os.Getenv(UserStateDirEnv); v != "" {
		return v
	}
	return filepath.Join(mustHomeDir(), ".kevin")
}

// ProjectStateDir returns the directory path for one environment's state,
// determined by an environment variable, or defaulting to ".kevin" (name
// == "") or ".kevin/<name>" under cwd.
func ProjectStateDir(cwd, name string) string {
	if v := os.Getenv(ProjectStateDirEnv); v != "" {
		return v
	}
	return filepath.Join(cwd, WorkspaceDir, name)
}

// File and directory names under a [Paths].
const (
	// LogsFile holds the durable, full record of every step's log lines for
	// one session, in newline-delimited JSON.
	LogsFile = "logs.ndjson"
	// RootCertFile is the root CA certificate, in the user state directory.
	RootCertFile = "root.crt"
	// CACertFile is the project intermediate CA certificate.
	CACertFile = "ca.crt"
	// CAKeyFile is the project intermediate CA private key.
	CAKeyFile = "ca.key"

	trustedKeysName       = "trusted-keys"
	trustedIdentitiesName = "trusted-identities"
	pkgCacheName          = "pkg-cache"
	pluginIndexName       = "plugin-index"
)

// Paths is every directory kevin reads or writes for one project
// environment. The zero value is not useful; build one with [For].
type Paths struct {
	// User is the per-user state directory, shared by every project.
	User string
	// Project is the state directory of one environment.
	Project string
	// Dir is the project directory.
	Dir string
}

// For returns the Paths of the environment called name in the project
// directory dir. A caller with no project passes empty strings and uses only
// the user-level paths.
func For(dir, name string) Paths {
	return Paths{
		User:    UserStateDir(),
		Project: ProjectStateDir(dir, name),
		Dir:     dir,
	}
}

// ForUser returns the Paths for a caller with no project; only the User
// paths are meaningful.
func ForUser() Paths { return For("", "") }

// Workspace is the directory plugins and kevin write the environment's files to.
func (p Paths) Workspace() string { return p.Project }

// Logs is the environment's durable step log.
func (p Paths) Logs() string { return filepath.Join(p.Project, LogsFile) }

// RootCert is the root CA certificate shared by every project.
func (p Paths) RootCert() string { return filepath.Join(p.User, RootCertFile) }

// CACert is the project's intermediate CA certificate.
func (p Paths) CACert() string { return filepath.Join(p.Project, CACertFile) }

// CAKey is the project's intermediate CA private key.
func (p Paths) CAKey() string { return filepath.Join(p.Project, CAKeyFile) }

// TrustedKeys holds the minisign public keys trusted to sign plugin packages.
func (p Paths) TrustedKeys() string { return filepath.Join(p.User, trustedKeysName) }

// TrustedIdentities holds the sigstore identities trusted to sign plugin packages.
func (p Paths) TrustedIdentities() string { return filepath.Join(p.User, trustedIdentitiesName) }

// PackageCache holds fetched plugin package blobs.
func (p Paths) PackageCache() string { return filepath.Join(p.User, pkgCacheName) }

// PluginIndex holds plugin index sources and their clones.
func (p Paths) PluginIndex() string { return filepath.Join(p.User, pluginIndexName) }

// Vars is the project.* CEL scope.
func (p Paths) Vars() map[string]string {
	return map[string]string{
		"dir":       p.Dir,
		"root_cert": p.RootCert(),
		"ca_cert":   p.CACert(),
		"ca_key":    p.CAKey(),
	}
}

func mustHomeDir() string {
	home, err := os.UserHomeDir()
	if err != nil {
		panic(err)
	}
	return home
}
