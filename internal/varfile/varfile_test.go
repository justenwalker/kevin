package varfile_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/justenwalker/kevin/internal/varfile"
)

func TestParse(t *testing.T) {
	t.Run("a well-formed file", func(t *testing.T) {
		values, err := varfile.Parse(strings.NewReader("region=us-east-1\napi_key=sk-123\n"))
		require.NoError(t, err)
		assert.Equal(t, map[string]string{"region": "us-east-1", "api_key": "sk-123"}, values)
	})

	t.Run("comments and blank lines are skipped", func(t *testing.T) {
		values, err := varfile.Parse(strings.NewReader("# a comment\n\nregion=us-east-1\n  # indented comment\n\n"))
		require.NoError(t, err)
		assert.Equal(t, map[string]string{"region": "us-east-1"}, values)
	})

	t.Run("key and value are trimmed of surrounding space", func(t *testing.T) {
		values, err := varfile.Parse(strings.NewReader("  region  =  us-east-1  \n"))
		require.NoError(t, err)
		assert.Equal(t, map[string]string{"region": "us-east-1"}, values)
	})

	t.Run("a value may itself contain an equals sign", func(t *testing.T) {
		values, err := varfile.Parse(strings.NewReader("dsn=postgres://u:p@host?sslmode=require\n"))
		require.NoError(t, err)
		assert.Equal(t, "postgres://u:p@host?sslmode=require", values["dsn"])
	})

	t.Run("a line with no equals sign errors", func(t *testing.T) {
		_, err := varfile.Parse(strings.NewReader("region\n"))
		require.Error(t, err)
		assert.Contains(t, err.Error(), "line 1")
	})

	t.Run("an empty input reports an empty, non-nil result", func(t *testing.T) {
		values, err := varfile.Parse(strings.NewReader(""))
		require.NoError(t, err)
		assert.Empty(t, values)
	})
}

func TestParseFile(t *testing.T) {
	t.Run("reads and parses a real file", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "vars.env")
		require.NoError(t, os.WriteFile(path, []byte("region=us-east-1\n"), 0o600))

		values, err := varfile.ParseFile(path)
		require.NoError(t, err)
		assert.Equal(t, map[string]string{"region": "us-east-1"}, values)
	})

	t.Run("a missing file errors", func(t *testing.T) {
		_, err := varfile.ParseFile(filepath.Join(t.TempDir(), "nope.env"))
		require.Error(t, err)
	})
}
