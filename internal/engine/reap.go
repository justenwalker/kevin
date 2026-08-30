package engine

import (
	"context"

	"github.com/justenwalker/kevin/internal/config"
	"github.com/justenwalker/kevin/internal/cri"
	"github.com/justenwalker/kevin/internal/relay"
)

// reap removes the docker resources of the project that no step removed. A
// crash of a plugin can leave a container behind, and the labels find it.
//
// reap skips a relay-role container (shutdown/Teardown own its lifecycle)
// and a still-live resource of the other scope (see otherScopeLive),
// leaving the shared project network in place when it finds one.
func (r *run) reap(ctx context.Context) error {
	names, err := dockerClient.ListByLabel(ctx, cri.LabelProject, r.cfg.Project)
	if err != nil {
		return err
	}
	relays, err := dockerClient.ListByLabel(ctx, cri.LabelRole, relay.Role)
	if err != nil {
		return err
	}

	skip := make(map[string]struct{}, len(relays))
	for _, name := range relays {
		skip[name] = struct{}{}
	}

	otherLive, err := r.otherScopeLive(ctx)
	if err != nil {
		return err
	}
	for name := range otherLive {
		skip[name] = struct{}{}
	}

	for _, name := range names {
		if _, ok := skip[name]; ok {
			continue
		}
		r.emit(name, "orphan, removing")
		if removeErr := dockerClient.Remove(ctx, name); removeErr != nil {
			return removeErr
		}
	}

	if len(otherLive) > 0 {
		return nil
	}
	return dockerClient.NetworkRemove(ctx, NetworkName(r.cfg.Project))
}

// otherScopeLive reports the container names of this project's other scope
// (setup or env) that are still live.
func (r *run) otherScopeLive(ctx context.Context) (map[string]struct{}, error) {
	otherScope := config.ScopeSetup
	if r.scope == config.ScopeSetup {
		otherScope = config.ScopeEnv
	}

	names, err := dockerClient.ListByLabel(ctx, cri.LabelScope, cri.ScopeLabel(r.cfg.Project, otherScope))
	if err != nil {
		return nil, err
	}

	live := make(map[string]struct{}, len(names))
	for _, name := range names {
		live[name] = struct{}{}
	}
	return live, nil
}
