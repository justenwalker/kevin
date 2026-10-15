package kubernetes

import "context"

// createOrRollBack runs create, and when it fails removes what it made with
// rollBack, unless retain is set.
func createOrRollBack(
	ctx context.Context, retain bool,
	create func(context.Context) ([]string, error),
	rollBack func(context.Context) error,
) ([]string, error) {
	nodes, err := create(ctx)
	if err != nil {
		if !retain {
			// A failed Up gets no Down, and the create error is the one to
			// report, so the rollback error is dropped.
			_ = rollBack(context.WithoutCancel(ctx))
		}
		return nil, err
	}
	return nodes, nil
}
