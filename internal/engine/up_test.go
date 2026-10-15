package engine

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"

	"github.com/justenwalker/kevin/internal/config"
	"github.com/justenwalker/kevin/internal/output"
	"github.com/justenwalker/kevin/internal/session"
	"github.com/justenwalker/kevin/internal/step"
)

func TestPlanUp(t *testing.T) {
	t.Run("builds the Up request from the step", func(t *testing.T) {
		r := newUpRun(t, newMockstepPlugin(t), newMockrouteTable(t), newMocktrafficRelay(t))
		step := r.steps["a"]
		step.With = json.RawMessage(`{"image":"nginx"}`)
		step.Timeout = config.Timeout(5 * time.Second)
		r.steps["a"] = step

		plan, err := r.planUp(t.Context(), "a", nil)

		require.NoError(t, err)
		assert.Equal(t, "a", plan.req.Step)
		assert.Equal(t, "echo", plan.req.Type)
		assert.JSONEq(t, `{"image":"nginx"}`, string(plan.req.Config))
		assert.Equal(t, 5*time.Second, plan.timeout)
		assert.Same(t, r.plugins["echo"], plan.client)
	})

	t.Run("a with block that does not render fails the step", func(t *testing.T) {
		r := newUpRun(t, newMockstepPlugin(t), newMockrouteTable(t), newMocktrafficRelay(t))
		step := r.steps["a"]
		step.With = json.RawMessage(`{"image":"${needs.missing.out.x}"}`)
		r.steps["a"] = step

		_, err := r.planUp(t.Context(), "a", nil)

		require.Error(t, err)
		assert.Equal(t, session.Failed, r.store.Snapshot().Steps[0].State)
	})
}

func TestCallUp(t *testing.T) {
	t.Run("an Up that outlives its timeout reports ErrStepTimeout", func(t *testing.T) {
		plugin := newMockstepPlugin(t)
		r := newUpRun(t, plugin, newMockrouteTable(t), newMocktrafficRelay(t))
		plugin.EXPECT().Up(mock.Anything, mock.Anything, mock.Anything).RunAndReturn(
			func(ctx context.Context, _ step.UpRequest, _ step.Events) (step.Result, error) {
				<-ctx.Done()
				return step.Result{}, ctx.Err()
			})

		_, err := r.callUp(t.Context(), upPlan{name: "a", client: plugin, req: step.UpRequest{}, timeout: time.Millisecond}, time.Now())

		require.ErrorIs(t, err, session.ErrStepTimeout)
		assert.Equal(t, session.Failed, r.store.Snapshot().Steps[0].State)
	})

	t.Run("a plugin error fails the step", func(t *testing.T) {
		plugin := newMockstepPlugin(t)
		r := newUpRun(t, plugin, newMockrouteTable(t), newMocktrafficRelay(t))
		plugin.EXPECT().Up(mock.Anything, mock.Anything, mock.Anything).Return(step.Result{}, assert.AnError)

		_, err := r.callUp(t.Context(), upPlan{name: "a", client: plugin, req: step.UpRequest{}}, time.Now())

		require.ErrorIs(t, err, assert.AnError)
		assert.Equal(t, session.Failed, r.store.Snapshot().Steps[0].State)
	})
}

func TestPublish(t *testing.T) {
	t.Run("a result with no routes or faults touches neither proxy nor relay", func(t *testing.T) {
		// The mocks fail the test on any unexpected call.
		routes := newMockrouteTable(t)
		r := newUpRun(t, newMockstepPlugin(t), routes, newMocktrafficRelay(t))
		routes.EXPECT().AllowEgress().Return()
		plan := upPlan{name: "a", ref: config.StepRef{Plugin: "echo", Step: "echo"}}
		result := step.Result{Outputs: output.Map{"url": {String: "http://a"}}}

		out, err := r.publish(t.Context(), plan, result, time.Now())

		require.NoError(t, err)
		assert.Equal(t, output.Map{"url": {String: "http://a"}}, out)
		assert.Equal(t, out, r.state.Snapshot()["a"])
		assert.Equal(t, session.Ready, r.store.Snapshot().Steps[0].State)
	})
}

func TestPublishReportsResult(t *testing.T) {
	t.Run("egress hosts and details reach the proxy and session", func(t *testing.T) {
		routes := newMockrouteTable(t)
		r := newUpRun(t, newMockstepPlugin(t), routes, newMocktrafficRelay(t))
		routes.EXPECT().AllowEgress([]string{"registry.example.com"}).Return()
		plan := upPlan{name: "a", ref: config.StepRef{Plugin: "echo", Step: "echo"}}
		result := step.Result{
			EgressAllow: []string{"registry.example.com"},
			Details:     []step.Detail{{Label: "url", Value: output.Value{String: "http://a"}}},
		}

		_, err := r.publish(t.Context(), plan, result, time.Now())

		require.NoError(t, err)
		details := r.store.Snapshot().Steps[0].Details
		require.Len(t, details, 1)
		assert.Equal(t, "url", details[0].Label)
		assert.Equal(t, "http://a", details[0].Value)
	})

	t.Run("a relay failure leaves the step not Ready", func(t *testing.T) {
		routes := newMockrouteTable(t)
		rl := newMocktrafficRelay(t)
		r := newUpRun(t, newMockstepPlugin(t), routes, rl)
		routes.EXPECT().AddRoutes(mock.Anything).Return()
		rl.EXPECT().EnsureListener(mock.Anything, "fake.example.com", []int{443}).Return(assert.AnError)
		plan := upPlan{name: "a", ref: config.StepRef{Plugin: "echo", Step: "echo"}}
		result := step.Result{
			Routes: []step.Route{{Host: "fake.example.com", Upstream: step.Upstream{Addr: "127.0.0.1:9090"}, Intercept: &step.Intercept{Ports: []int{443}}}},
		}

		_, err := r.publish(t.Context(), plan, result, time.Now())

		require.ErrorIs(t, err, assert.AnError)
		assert.NotEqual(t, session.Ready, r.store.Snapshot().Steps[0].State)
	})
}
