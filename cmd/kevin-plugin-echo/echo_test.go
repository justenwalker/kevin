package main

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/justenwalker/kevin/plugin"
)

func TestSchema(t *testing.T) {
	assert.Contains(t, string(echo{}.Schema()), "#Config", "the schema must declare #Config")
}

func TestUp(t *testing.T) {
	t.Run("publishes the step name and the configured outputs", func(t *testing.T) {
		out := &capture{}
		result, err := echo{}.Up(t.Context(), &plugin.UpRequest{
			Step:   "api",
			Config: []byte(`{"message":"hello","outputs":{"port":"8080"}}`),
		}, out)
		require.NoError(t, err)

		assert.Equal(t, plugin.StringMap(map[string]string{"port": "8080", "step": "api"}), result.Outputs)
		assert.Equal(t, []string{"hello"}, out.logs)
	})

	t.Run("no config provided", func(t *testing.T) {
		out := &capture{}
		result, err := echo{}.Up(t.Context(), &plugin.UpRequest{Step: "bare"}, out)
		require.NoError(t, err)

		assert.Equal(t, plugin.StringMap(map[string]string{"step": "bare"}), result.Outputs)
		assert.Empty(t, out.logs, "an absent message writes no line")
	})

	t.Run("logs outputs in a stable order", func(t *testing.T) {
		out := &capture{}
		_, err := echo{}.Up(t.Context(), &plugin.UpRequest{
			Step: "d",
			Deps: map[string]map[string]plugin.Value{
				"c": plugin.StringMap(map[string]string{"k": "3"}),
				"a": plugin.StringMap(map[string]string{"k": "1"}),
				"b": plugin.StringMap(map[string]string{"k": "2"}),
			},
		}, out)
		require.NoError(t, err)

		require.Len(t, out.logs, 3)
		assert.Contains(t, out.logs[0], "saw a")
		assert.Contains(t, out.logs[1], "saw b")
		assert.Contains(t, out.logs[2], "saw c")
	})

	t.Run("delay", func(t *testing.T) {
		out := &capture{}
		start := time.Now()

		_, err := echo{}.Up(t.Context(), &plugin.UpRequest{
			Step:   "slow",
			Config: []byte(`{"delay":"30ms"}`),
		}, out)
		require.NoError(t, err)

		assert.GreaterOrEqual(t, time.Since(start), 30*time.Millisecond)
		assert.Equal(t, []string{"waiting"}, out.progress)
	})

	t.Run("stop on context cancelled", func(t *testing.T) {
		ctx, cancel := context.WithCancel(t.Context())
		cancel()

		_, err := echo{}.Up(ctx, &plugin.UpRequest{
			Step:   "slow",
			Config: []byte(`{"delay":"1m"}`),
		}, &capture{})
		require.ErrorIs(t, err, ctx.Err())
	})

	t.Run("failures", func(t *testing.T) {
		tests := []struct {
			name   string
			config string
			expect string
		}{
			{
				name:   "the configuration asks for a failure",
				config: `{"fail":true}`,
				expect: "failure requested",
			},
			{
				name:   "the delay is not a duration",
				config: `{"delay":"soon"}`,
				expect: "delay",
			},
			{
				name:   "the configuration is not JSON",
				config: `{`,
				expect: "decode config",
			},
		}

		for _, tt := range tests {
			t.Run(tt.name, func(t *testing.T) {
				_, err := echo{}.Up(t.Context(), &plugin.UpRequest{
					Step:   "a",
					Config: []byte(tt.config),
				}, &capture{})
				require.Error(t, err)
				assert.Contains(t, err.Error(), tt.expect)
			})
		}
	})
}

func TestDown(t *testing.T) {
	out := &capture{}
	require.NoError(t, echo{}.Down(t.Context(), &plugin.DownRequest{Step: "api"}, out))
	assert.Equal(t, []string{"removing api"}, out.logs)
}

// capture records what a Step emits.
type capture struct {
	logs     []string
	progress []string
}

func (c *capture) Log(_, text string) { c.logs = append(c.logs, text) }

func (c *capture) Progress(label string, _, _ int64) {
	c.progress = append(c.progress, label)
}
