package kubernetes

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/justenwalker/kevin/internal/cri"
)

func TestJoinProjectNetwork(t *testing.T) {
	onNetwork := func(network string) func(context.Context, string) (cri.Container, error) {
		return func(context.Context, string) (cri.Container, error) {
			return cri.Container{IPs: map[string]string{network: "10.0.0.2"}}, nil
		}
	}

	t.Run("an empty network skips the step", func(t *testing.T) {
		rt := fakeRuntime{networkConnect: func(context.Context, string, string) error {
			assert.Fail(t, "must not connect when no network was asked for")
			return nil
		}}
		require.NoError(t, joinProjectNetwork(t.Context(), rt, []string{"demo-cluster"}, ""))
	})

	t.Run("connects every node and checks its membership", func(t *testing.T) {
		var seen []string
		rt := fakeRuntime{
			networkConnect: func(_ context.Context, network, container string) error {
				assert.Equal(t, "kevin-demo", network)
				seen = append(seen, container)
				return nil
			},
			inspect: onNetwork("kevin-demo"),
		}
		require.NoError(t, joinProjectNetwork(t.Context(), rt, []string{"demo-cluster", "demo-cluster-m02"}, "kevin-demo"))
		assert.Equal(t, []string{"demo-cluster", "demo-cluster-m02"}, seen)
	})

	t.Run("a connect failure is an error and stops at that node", func(t *testing.T) {
		var seen []string
		rt := fakeRuntime{networkConnect: func(_ context.Context, _, container string) error {
			seen = append(seen, container)
			return errors.New("no such network")
		}}
		err := joinProjectNetwork(t.Context(), rt, []string{"demo-cluster", "demo-cluster-m02"}, "kevin-demo")
		require.Error(t, err)
		assert.Equal(t, []string{"demo-cluster"}, seen, "a node after the failure is never reached")
	})

	t.Run("a node that is not on the network after the connect is an error", func(t *testing.T) {
		rt := fakeRuntime{inspect: onNetwork("bridge")}
		err := joinProjectNetwork(t.Context(), rt, []string{"demo-cluster"}, "kevin-demo")
		require.ErrorIs(t, err, ErrNetworkMismatch)
	})

	t.Run("an inspect failure is an error", func(t *testing.T) {
		rt := fakeRuntime{inspect: func(context.Context, string) (cri.Container, error) {
			return cri.Container{}, errors.New("no such container")
		}}
		require.Error(t, joinProjectNetwork(t.Context(), rt, []string{"demo-cluster"}, "kevin-demo"))
	})

	t.Run("a node on the project network and its own network passes", func(t *testing.T) {
		rt := fakeRuntime{inspect: func(context.Context, string) (cri.Container, error) {
			return cri.Container{IPs: map[string]string{"kevin-demo": "10.0.0.2", "kind": "172.18.0.2"}}, nil
		}}
		require.NoError(t, joinProjectNetwork(t.Context(), rt, []string{"demo-cluster"}, "kevin-demo"))
	})
}
