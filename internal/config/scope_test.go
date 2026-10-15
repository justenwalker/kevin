package config

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestScope(t *testing.T) {
	t.Run("Valid is true only for the two scopes", func(t *testing.T) {
		assert.True(t, ScopeEnv.Valid())
		assert.True(t, ScopeSetup.Valid())
		assert.False(t, Scope(0).Valid())
		assert.False(t, Scope(3).Valid())
	})

	t.Run("String names each scope", func(t *testing.T) {
		assert.Equal(t, "env", ScopeEnv.String())
		assert.Equal(t, "setup", ScopeSetup.String())
	})
}
