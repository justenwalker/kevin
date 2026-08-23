package state

import (
	"os"
	"path/filepath"
)

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
	return filepath.Join(cwd, ".kevin", name)
}

func mustHomeDir() string {
	home, err := os.UserHomeDir()
	if err != nil {
		panic(err)
	}
	return home
}
