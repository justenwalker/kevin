package engine

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"

	"github.com/justenwalker/kevin/internal/config"
	"github.com/justenwalker/kevin/internal/output"
	"github.com/justenwalker/kevin/internal/pluginhost"
	"github.com/justenwalker/kevin/internal/runapi"
	"github.com/justenwalker/kevin/internal/session"
	"github.com/justenwalker/kevin/protos/pb"
)

func TestRerun(t *testing.T) {
	t.Run("reports the step and the proxy requests denied while it ran", func(t *testing.T) {
		plugin := newMockstepPlugin(t)
		routes := newMockrouteTable(t)
		r := newUpRun(t, plugin, routes, newMocktrafficRelay(t))
		r.store.Record(session.Request{Time: time.Now().Add(-time.Hour), Denied: true, Host: "stale.example.com"})
		plugin.EXPECT().Up(mock.Anything, mock.Anything, mock.Anything).RunAndReturn(
			func(context.Context, *pb.UpRequest, func(*pb.Event)) (*pb.Result, error) {
				r.store.Record(session.Request{Time: time.Now().Add(time.Minute), Denied: true, Method: "GET", Host: "blocked.example.com", Path: "/data"})
				r.store.Record(session.Request{Time: time.Now().Add(time.Minute), Host: "allowed.example.com"})
				return &pb.Result{EgressAllow: []string{"x.example.com"}}, nil
			})
		routes.EXPECT().AllowEgress([]string{"x.example.com"}).Return()

		got, err := r.Rerun(t.Context(), runapi.RerunRequest{Step: "a"})

		require.NoError(t, err)
		assert.Equal(t, "a", got.Step)
		assert.False(t, got.Failed)
		require.Len(t, got.Changed, 1, "the targeted step is always reported")
		assert.Equal(t, "a", got.Changed[0].Name)
		assert.Equal(t, "ready", got.Changed[0].State)
		require.Len(t, got.Denied, 1)
		assert.Equal(t, "blocked.example.com", got.Denied[0].Host)
		assert.Equal(t, "/data", got.Denied[0].Path)
	})

	t.Run("a step that ran and failed is a result, not an error", func(t *testing.T) {
		plugin := newMockstepPlugin(t)
		r := newUpRun(t, plugin, newMockrouteTable(t), newMocktrafficRelay(t))
		plugin.EXPECT().Up(mock.Anything, mock.Anything, mock.Anything).Return(nil, assert.AnError)

		got, err := r.Rerun(t.Context(), runapi.RerunRequest{Step: "a"})

		require.NoError(t, err)
		assert.True(t, got.Failed)
		require.Len(t, got.Changed, 1)
		assert.Equal(t, "failed", got.Changed[0].State)
		assert.NotEmpty(t, got.Changed[0].Message)
	})

	t.Run("a canceled context is an error, not a result", func(t *testing.T) {
		plugin := newMockstepPlugin(t)
		r := newUpRun(t, plugin, newMockrouteTable(t), newMocktrafficRelay(t))
		ctx, cancel := context.WithCancel(t.Context())
		plugin.EXPECT().Up(mock.Anything, mock.Anything, mock.Anything).RunAndReturn(
			func(context.Context, *pb.UpRequest, func(*pb.Event)) (*pb.Result, error) {
				cancel()
				return nil, context.Canceled
			})

		_, err := r.Rerun(ctx, runapi.RerunRequest{Step: "a"})

		require.ErrorIs(t, err, context.Canceled)
	})

	t.Run("a cascaded dependent that fails is a result", func(t *testing.T) {
		plugin := newMockstepPlugin(t)
		routes := newMockrouteTable(t)
		r := newUpRun(t, plugin, routes, newMocktrafficRelay(t))
		r.steps["b"] = config.Step{Uses: config.StepRef{Plugin: "echo", Step: "echo"}, Needs: []config.Need{{Step: "a"}}}
		require.NoError(t, r.registerScopeSteps(r.store))
		r.state.Complete(map[string]output.Map{"a": nil, "b": nil})
		r.caps = map[string]pluginhost.Info{"echo": {Steps: []pluginhost.StepInfo{{Name: "echo", Idempotent: true}}}}
		plugin.EXPECT().Up(mock.Anything, mock.Anything, mock.Anything).RunAndReturn(
			func(_ context.Context, req *pb.UpRequest, _ func(*pb.Event)) (*pb.Result, error) {
				if req.GetStep() == "b" {
					return nil, assert.AnError
				}
				return &pb.Result{EgressAllow: []string{"x.example.com"}}, nil
			})
		routes.EXPECT().AllowEgress([]string{"x.example.com"}).Return()

		got, err := r.Rerun(t.Context(), runapi.RerunRequest{Step: "a", Cascade: true})

		require.NoError(t, err)
		assert.False(t, got.Failed, "the targeted step succeeded")
		names := make(map[string]string)
		for _, st := range got.Changed {
			names[st.Name] = st.State
		}
		assert.Equal(t, "ready", names["a"])
		assert.Equal(t, "failed", names["b"])
	})

	t.Run("rejects an unknown step", func(t *testing.T) {
		r := newUpRun(t, newMockstepPlugin(t), newMockrouteTable(t), newMocktrafficRelay(t))

		_, err := r.Rerun(t.Context(), runapi.RerunRequest{Step: "nope"})

		require.ErrorIs(t, err, session.ErrUnknownStep)
	})

	t.Run("rejects a step that is already running", func(t *testing.T) {
		r := newUpRun(t, newMockstepPlugin(t), newMockrouteTable(t), newMocktrafficRelay(t))
		unlock, ok := r.state.TryLock("a")
		require.True(t, ok)
		defer unlock()

		_, err := r.Rerun(t.Context(), runapi.RerunRequest{Step: "a"})

		require.ErrorIs(t, err, session.ErrStepBusy)
	})
}
