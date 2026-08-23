package engine

import (
	"context"

	"github.com/justenwalker/kevin/internal/cri"
	"github.com/justenwalker/kevin/internal/relay"
)

// reap removes the docker resources of the project that no step removed. A
// crash of a plugin can leave a container behind, and the labels find it.
//
// reap skips a container that carries the relay role. The caller stops the
// relay itself, and reaping it here would race the removal of the network
// that the relay still joins.
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

	for _, name := range names {
		if _, ok := skip[name]; ok {
			continue
		}
		r.emit(name, "orphan, removing")
		if removeErr := dockerClient.Remove(ctx, name); removeErr != nil {
			return removeErr
		}
	}
	return dockerClient.NetworkRemove(ctx, NetworkName(r.cfg.Project))
}
