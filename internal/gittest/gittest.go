// Package gittest holds helpers for tests that run the git binary.
package gittest

import "os"

// repoVars are the variables that point git at a specific repository.
var repoVars = []string{"GIT_DIR", "GIT_WORK_TREE", "GIT_INDEX_FILE", "GIT_PREFIX", "GIT_COMMON_DIR"}

// Isolate unsets the variables that select a repository, so every git
// child of the test process acts on its own working directory. Call it
// from TestMain.
func Isolate() {
	for _, name := range repoVars {
		_ = os.Unsetenv(name)
	}
}
