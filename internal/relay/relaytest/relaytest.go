// Package relaytest builds the relay image from this checkout for tests that
// start a real relay.
package relaytest

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sync"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/justenwalker/kevin/internal/relay"
)

// Tag is the image that Build produces. It matches RelayImageTag in
// build/main.go.
const Tag = "kevin-relay:dev"

var buildOnce = sync.OnceValues(build)

// Build cross-compiles kevin-relay for linux/GOARCH and builds it into Tag,
// the same way as the relay-image build target. It builds once per test
// binary, and always from source, so a test never runs an older image that
// happens to carry the same tag.
func Build() (string, error) {
	return buildOnce()
}

// UseDevImage builds Tag with Build and points relay.Ref at it for the rest
// of t. It fails t when the image does not build.
func UseDevImage(t *testing.T) {
	t.Helper()

	tag, err := Build()
	require.NoError(t, err)
	t.Setenv(relay.ImageEnvVar, tag)
}

func build() (string, error) {
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		return "", ErrNoRepoRoot
	}
	root := filepath.Join(filepath.Dir(file), "..", "..", "..")

	dir, err := os.MkdirTemp("", "kevin-relay-image")
	if err != nil {
		return "", fmt.Errorf("relaytest: %w", err)
	}
	defer os.RemoveAll(dir) //nolint:errcheck // best effort cleanup of a temp directory

	bin := filepath.Join(dir, "linux", runtime.GOARCH, "kevin-relay")
	goBuild := exec.CommandContext(context.Background(), "go", "build", "-o", bin, "./cmd/kevin-relay") //nolint:gosec // bin is a path under a fresh temp directory
	goBuild.Dir = root
	goBuild.Env = append(os.Environ(), "GOOS=linux", "GOARCH="+runtime.GOARCH, "CGO_ENABLED=0")
	if out, err := goBuild.CombinedOutput(); err != nil {
		return "", fmt.Errorf("relaytest: build kevin-relay: %w: %s", err, out)
	}

	//nolint:gosec // every argument is fixed or derived from this checkout
	dockerBuild := exec.CommandContext(context.Background(), "docker", "build",
		"-f", filepath.Join(root, "build", "relay.Dockerfile"),
		"--build-arg", "TARGETARCH="+runtime.GOARCH,
		"-t", Tag, dir)
	if out, err := dockerBuild.CombinedOutput(); err != nil {
		return "", fmt.Errorf("relaytest: docker build %s: %w: %s", Tag, err, out)
	}
	return Tag, nil
}
