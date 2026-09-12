// Package engines picks a [cri.Runtime] by name, so every caller that
// resolves the host's selected container engine (the container and kind
// plugins, the engine package's own network/proxy setup, the relay, "kevin
// doctor") does it the same way instead of duplicating the switch.
package engines

import (
	"context"
	"fmt"

	"github.com/justenwalker/kevin/internal/cri"
	"github.com/justenwalker/kevin/internal/docker"
	"github.com/justenwalker/kevin/internal/podman"
)

// New builds the [cri.Runtime] that name identifies, decoding configBytes as
// that engine's own config message. An empty name means "docker".
func New(name string, configBytes []byte) (cri.Runtime, error) {
	switch name {
	case "", "docker":
		return docker.New(configBytes)
	case "podman":
		return podman.New(configBytes)
	default:
		return nil, fmt.Errorf("%w: %q", ErrUnsupported, name)
	}
}

// Detect finds whichever supported engine is actually reachable on this
// host - docker first when both are, matching the long-standing default.
// It probes each engine's own Available check (the CLI runs and its daemon
// answers), the same check "kevin doctor" runs, rather than only checking
// PATH: a binary present with no running daemon is common (Docker Desktop
// not started, a podman machine not up) and should fall through to the
// other engine, not report false.
func Detect(ctx context.Context) (string, error) {
	for _, name := range []string{"docker", "podman"} {
		rt, err := New(name, nil)
		if err != nil {
			continue
		}
		if rt.Available(ctx) == nil {
			return name, nil
		}
	}
	return "", ErrNoEngine
}
