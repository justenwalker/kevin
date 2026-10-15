package config

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestNeed(t *testing.T) {
	t.Run("ParseNeed reads the setup prefix", func(t *testing.T) {
		tests := []struct {
			in   string
			want Need
		}{
			{in: "db", want: Need{Step: "db"}},
			{in: "setup.cluster", want: Need{Setup: true, Step: "cluster"}},
			{in: "setup.", want: Need{Setup: true}},
			{in: "setupdb", want: Need{Step: "setupdb"}},
			{in: "a.b", want: Need{Step: "a.b"}},
		}
		for _, tt := range tests {
			got := ParseNeed(tt.in)
			assert.Equal(t, tt.want, got, tt.in)
			assert.Equal(t, tt.in, got.String(), "String is the inverse of ParseNeed")
		}
	})

	t.Run("decodes from JSON strings", func(t *testing.T) {
		var needs []Need
		require.NoError(t, json.Unmarshal([]byte(`["a","setup.b"]`), &needs))
		assert.Equal(t, []Need{{Step: "a"}, {Setup: true, Step: "b"}}, needs)
	})

	t.Run("NeedStrings renders each entry", func(t *testing.T) {
		assert.Equal(t, []string{"a", "setup.b"}, NeedStrings([]Need{{Step: "a"}, {Setup: true, Step: "b"}}))
		assert.Empty(t, NeedStrings(nil))
	})
}
