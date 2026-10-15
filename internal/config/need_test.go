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

func TestResolveNeed(t *testing.T) {
	steps := map[string]Step{"web": {}, "db.primary": {}, "db.replica": {}}
	groups := map[string]Group{"db": {Members: []string{"primary", "replica"}}}

	tests := []struct {
		name string
		from string
		dep  Need
		want string
		ok   bool
	}{
		{name: "a plain step", from: "web", dep: Need{Step: "web"}, want: "web", ok: true},
		{name: "a group by its own name", from: "web", dep: Need{Step: "db"}, want: "db", ok: true},
		{name: "a sibling by its bare name", from: "db.replica", dep: Need{Step: "primary"}, want: "db.primary", ok: true},
		{name: "a member naming a plain step", from: "db.replica", dep: Need{Step: "web"}, want: "web", ok: true},
		{name: "a dotted name outside any group", from: "x.y", dep: Need{Step: "primary"}},
		{name: "a setup entry", from: "web", dep: Need{Setup: true, Step: "web"}},
		{name: "an unknown name", from: "web", dep: Need{Step: "missing"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, ok := ResolveNeed(tt.from, tt.dep, steps, groups)

			assert.Equal(t, tt.ok, ok)
			assert.Equal(t, tt.want, got)
		})
	}
}
