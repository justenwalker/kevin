package cueschema_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/justenwalker/kevin/internal/cueschema"
)

const testSchema = `#Config: {
	// name is required, with no default.
	name!: string

	// greeting says hello, with a default.
	greeting?: string | *"hello"

	// count has no default at all.
	count?: int

	// tags is a list.
	tags?: [...string]

	// labels is a pattern-constraint map.
	labels?: [string]: string

	// protocol picks a transport, such as "tcp" or "udp".
	protocol?: "tcp" | "udp" | *"tcp"

	// hosts references another definition.
	hosts?: [...#Host]

	// wrapped spans two lines in the source, and mentions a "quoted value"
	// plus a <placeholder>.<pair>.
	wrapped?: string

	// nested has a "socks5://<relay>/<host:port>" URL, a placeholder inside a quoted string.
	nested?: string
}

#Host: {
	// addr is required.
	addr!: string
}
`

func TestParseReducesEachFieldOfTheRealShapeAConfigSchemaTakes(t *testing.T) {
	path := filepath.Join(t.TempDir(), "schema.cue")
	require.NoError(t, os.WriteFile(path, []byte(testSchema), 0o600))

	schema, err := cueschema.Parse(path)
	require.NoError(t, err)

	require.Contains(t, schema.Definitions, "Config")
	require.Contains(t, schema.Definitions, "Host")
	assert.Equal(t, "#Host", schema.Definitions["Host"].Name)

	fields := fieldsByName(t, schema.Definitions["Config"].Fields)

	t.Run("a required field with no default", func(t *testing.T) {
		f := fields["name"]
		assert.True(t, f.Required)
		assert.Equal(t, "string", f.Type)
		assert.Empty(t, f.Default)
		assert.Equal(t, "Required, with no default.", f.Doc)
	})

	t.Run("an optional field with an explicit default", func(t *testing.T) {
		f := fields["greeting"]
		assert.False(t, f.Required)
		assert.Equal(t, "string", f.Type)
		assert.Equal(t, `"hello"`, f.Default)
	})

	t.Run("an optional field with no default", func(t *testing.T) {
		f := fields["count"]
		assert.False(t, f.Required)
		assert.Equal(t, "int", f.Type)
		assert.Empty(t, f.Default)
	})

	t.Run("a list type", func(t *testing.T) {
		assert.Equal(t, "[...string]", fields["tags"].Type)
	})

	t.Run("a pattern-constraint map keeps its shorthand, not the expanded struct", func(t *testing.T) {
		assert.Equal(t, "[string]: string", fields["labels"].Type)
	})

	t.Run("an enum deduplicates the default alternative CUE repeats", func(t *testing.T) {
		f := fields["protocol"]
		assert.Empty(t, f.Type)
		assert.Equal(t, []string{`"tcp"`, `"udp"`}, f.Enum)
		assert.Equal(t, `"tcp"`, f.Default)
	})

	t.Run("a reference to another definition keeps the reference, not its inlined body", func(t *testing.T) {
		assert.Equal(t, "[...#Host]", fields["hosts"].Type)
	})

	t.Run("doc text collapses embedded newlines and backtick-wraps quotes and placeholders", func(t *testing.T) {
		got := fields["wrapped"].Doc
		assert.NotContains(t, got, "\n")
		assert.Contains(t, got, "`\"quoted value\"`")
		assert.Contains(t, got, "`<placeholder>.<pair>`")
	})

	t.Run("a placeholder inside a quoted string stays in one code span, not nested", func(t *testing.T) {
		got := fields["nested"].Doc
		assert.Contains(t, got, "`\"socks5://<relay>/<host:port>\"`")
	})
}

func fieldsByName(t *testing.T, fs []cueschema.Field) map[string]cueschema.Field {
	t.Helper()
	m := make(map[string]cueschema.Field, len(fs))
	for _, f := range fs {
		m[f.Name] = f
	}
	return m
}
