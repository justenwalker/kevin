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
	"github.com/justenwalker/kevin/internal/proxy"
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

func TestProxyRoute(t *testing.T) {
	tests := []struct {
		name string
		mode step.RouteMode
		want proxy.RouteMode
	}{
		{name: "mitm", mode: step.RouteModeMITM, want: proxy.RouteModeMITM},
		{name: "passthrough", mode: step.RouteModePassthrough, want: proxy.RouteModePassthrough},
		{name: "raw", mode: step.RouteModeRaw, want: proxy.RouteModeRaw},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := proxyRoute(step.Route{Host: "a.test", Mode: tt.mode})

			assert.Equal(t, tt.want, got.Mode)
		})
	}

	t.Run("a relay upstream names the target and the relay", func(t *testing.T) {
		got := proxyRoute(step.Route{Host: "a.test", Upstream: step.Upstream{Relay: &step.RelayTarget{Relay: "r:1080", Target: "svc:80"}}})

		assert.Equal(t, "svc:80", got.Upstream)
		assert.Equal(t, "r:1080", got.Relay)
	})

	t.Run("a direct upstream has no relay", func(t *testing.T) {
		got := proxyRoute(step.Route{Host: "a.test", Upstream: step.Upstream{Addr: "127.0.0.1:80"}})

		assert.Equal(t, "127.0.0.1:80", got.Upstream)
		assert.Empty(t, got.Relay)
	})
}

func TestUpStep(t *testing.T) {
	t.Run("publishes a step's result to the proxy, relay and session", func(t *testing.T) {
		plugin := newMockstepPlugin(t)
		routes := newMockrouteTable(t)
		rl := newMocktrafficRelay(t)
		r := newUpRun(t, plugin, routes, rl)

		plugin.EXPECT().Up(mock.Anything, mock.Anything, mock.Anything).Return(step.Result{
			Outputs: output.Map{"url": {String: "http://a"}},
			Routes: []step.Route{
				{Host: "a.kevin.test", Upstream: step.Upstream{Addr: "127.0.0.1:8080"}, TLS: true},
				{Host: "fake.example.com", Upstream: step.Upstream{Addr: "127.0.0.1:9090"}, Intercept: &step.Intercept{Ports: []int{443}}},
			},
			EgressAllow: []string{"registry.example.com"},
			Faults:      []step.Fault{{ID: "f1"}},
		}, nil)
		routes.EXPECT().AddRoutes([]proxy.Route{{Host: "a.kevin.test", Upstream: "127.0.0.1:8080", TLS: true, Mode: proxy.RouteModeMITM}}).Return()
		routes.EXPECT().AddRoutes([]proxy.Route{{Host: "fake.example.com", Upstream: "127.0.0.1:9090", Mode: proxy.RouteModeMITM}}).Return()
		routes.EXPECT().AllowEgress([]string{"registry.example.com"}).Return()
		rl.EXPECT().EnsureListener(mock.Anything, "fake.example.com", []int{443}).Return(nil)
		rl.EXPECT().ApplyFault(mock.Anything, mock.Anything).Return(nil)

		out, err := r.upStep(t.Context(), "a", nil)

		require.NoError(t, err)
		assert.Equal(t, output.Map{"url": {String: "http://a"}}, out)
		assert.Equal(t, []string{"f1"}, r.state.faults["a"])
		assert.Equal(t, session.Ready, r.store.Snapshot().Steps[0].State)
	})

	t.Run("a relay failure fails the step", func(t *testing.T) {
		plugin := newMockstepPlugin(t)
		routes := newMockrouteTable(t)
		rl := newMocktrafficRelay(t)
		r := newUpRun(t, plugin, routes, rl)
		plugin.EXPECT().Up(mock.Anything, mock.Anything, mock.Anything).Return(step.Result{
			Routes: []step.Route{{Host: "fake.example.com", Upstream: step.Upstream{Addr: "127.0.0.1:9090"}, Intercept: &step.Intercept{Ports: []int{443}}}},
		}, nil)
		routes.EXPECT().AddRoutes(mock.Anything).Return()
		rl.EXPECT().EnsureListener(mock.Anything, "fake.example.com", []int{443}).Return(assert.AnError)

		_, err := r.upStep(t.Context(), "a", nil)

		require.ErrorIs(t, err, assert.AnError)
		assert.Equal(t, session.Failed, r.store.Snapshot().Steps[0].State)
	})

	t.Run("a failed Up publishes nothing", func(t *testing.T) {
		plugin := newMockstepPlugin(t)
		r := newUpRun(t, plugin, newMockrouteTable(t), newMocktrafficRelay(t))
		plugin.EXPECT().Up(mock.Anything, mock.Anything, mock.Anything).Return(step.Result{}, assert.AnError)

		_, err := r.upStep(t.Context(), "a", nil)

		require.ErrorIs(t, err, assert.AnError)
		assert.Equal(t, session.Failed, r.store.Snapshot().Steps[0].State)
	})

	t.Run("a step that is already running reports ErrStepBusy", func(t *testing.T) {
		r := newUpRun(t, newMockstepPlugin(t), newMockrouteTable(t), newMocktrafficRelay(t))
		unlock, ok := r.state.TryLock("a")
		require.True(t, ok)
		defer unlock()

		_, err := r.upStep(t.Context(), "a", nil)

		require.ErrorIs(t, err, session.ErrStepBusy)
	})
}
