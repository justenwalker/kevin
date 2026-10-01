package kubernetes

import (
	"context"
	"fmt"
	"time"

	"github.com/justenwalker/kevin/internal/cri"
)

// containerdReadyTimeout bounds the wait for containerd after a restart.
const containerdReadyTimeout = 30 * time.Second

// containerdPollInterval is the wait between two checks of containerd.
const containerdPollInterval = 200 * time.Millisecond

// waitContainerdReady polls a node until containerd answers again.
// waitContainerdReady returns ErrContainerdNotReady when the timeout passes
// first.
func waitContainerdReady(ctx context.Context, rt cri.Runtime, container string) error {
	deadline := time.Now().Add(containerdReadyTimeout)
	for {
		if _, err := rt.Exec(ctx, container, "ctr", "version"); err == nil {
			return nil
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("%s: %w", container, ErrContainerdNotReady)
		}

		select {
		case <-time.After(containerdPollInterval):
		case <-ctx.Done():
			return ctx.Err() //nolint:wrapcheck // context.Canceled/DeadlineExceeded is the idiomatic bare sentinel
		}
	}
}
