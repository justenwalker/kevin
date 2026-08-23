package logging_test

import (
	"bytes"
	"encoding/json"
	"log/slog"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/justenwalker/kevin/internal/logging"
)

func TestWithAttrs(t *testing.T) {
	t.Run("Attrs is empty for a bare context", func(t *testing.T) {
		assert.Empty(t, logging.Attrs(t.Context()))
	})

	t.Run("keeps the context when there is nothing to add", func(t *testing.T) {
		ctx := t.Context()
		assert.Equal(t, ctx, logging.WithAttrs(ctx))
	})

	t.Run("accumulates", func(t *testing.T) {
		ctx := logging.WithAttrs(t.Context(), slog.String("step", "api"))
		ctx = logging.WithAttrs(ctx, slog.String("plugin", "container"))

		got := map[string]string{}
		for _, a := range logging.Attrs(ctx) {
			got[a.Key] = a.Value.String()
		}
		assert.Equal(t, map[string]string{"step": "api", "plugin": "container"}, got)
	})

	t.Run("replaces a key instead of repeating it", func(t *testing.T) {
		ctx := logging.WithAttrs(t.Context(), slog.String("step", "old"), slog.Int("try", 1))
		ctx = logging.WithAttrs(ctx, slog.String("step", "new"))

		attrs := logging.Attrs(ctx)
		require.Len(t, attrs, 2, "a repeated key must not appear two times")

		got := map[string]string{}
		for _, a := range attrs {
			got[a.Key] = a.Value.String()
		}
		assert.Equal(t, "new", got["step"])
		assert.Equal(t, "1", got["try"], "an unrelated key must survive")
	})
}

func TestCtxLogger(t *testing.T) {
	t.Run("carries the package and the attributes", func(t *testing.T) {
		var buf bytes.Buffer
		slog.SetDefault(slog.New(slog.NewJSONHandler(&buf, nil)))

		log := logging.New("proxy")
		ctx := logging.WithAttrs(t.Context(), slog.String("step", "api"))
		log.Ctx(ctx).Info("routed")

		var record map[string]any
		require.NoError(t, json.Unmarshal(buf.Bytes(), &record))
		assert.Equal(t, "proxy", record["package"])
		assert.Equal(t, "api", record["step"])
		assert.Equal(t, "routed", record["msg"])
	})

	t.Run("works without attributes", func(t *testing.T) {
		var buf bytes.Buffer
		slog.SetDefault(slog.New(slog.NewJSONHandler(&buf, nil)))

		logging.New("dag").Ctx(t.Context()).Info("walked")

		var record map[string]any
		require.NoError(t, json.Unmarshal(buf.Bytes(), &record))
		assert.Equal(t, "dag", record["package"])
		assert.NotContains(t, record, "step")
	})
}
