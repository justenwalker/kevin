// Package engines picks a [cri.Runtime] by engine, so every caller that
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
	"github.com/justenwalker/kevin/internal/step"
	"github.com/justenwalker/kevin/plugin"
)

// Parse returns the [step.Engine] that name selects. An empty name means
// "docker".
func Parse(name string) (step.Engine, error) {
	switch name {
	case "", "docker":
		return step.EngineDocker, nil
	case "podman":
		return step.EnginePodman, nil
	default:
		return step.EngineDocker, fmt.Errorf("%w: %q", ErrUnsupported, name)
	}
}

// For returns the [cri.Runtime] for a host-side engine.
func For(e step.Engine) cri.Runtime {
	if e == step.EnginePodman {
		return podman.Client{}
	}
	return docker.Client{}
}

// Of returns the [cri.Runtime] for a plugin's engine. It cannot fail: a
// [plugin.Engine] only holds engines kevin implements.
func Of(e plugin.Engine) cri.Runtime {
	if e == plugin.EnginePodman {
		return podman.Client{}
	}
	return docker.Client{}
}

// Detect finds whichever supported engine is actually reachable on this
// host - docker first when both are, matching the long-standing default.
// It probes each engine's own Available check (the CLI runs and its daemon
// answers), the same check "kevin doctor" runs, rather than only checking
// PATH: a binary present with no running daemon is common (Docker Desktop
// not started, a podman machine not up) and should fall through to the
// other engine, not report false.
func Detect(ctx context.Context) (step.Engine, error) {
	for _, e := range []step.Engine{step.EngineDocker, step.EnginePodman} {
		if For(e).Available(ctx) == nil {
			return e, nil
		}
	}
	return step.EngineDocker, ErrNoEngine
}
