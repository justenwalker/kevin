package plugin

import (
	"fmt"
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestValueRedaction(t *testing.T) {
	public := String("plain")
	secret := Sensitive{String("hunter2")}

	assert.Equal(t, "plain", fmt.Sprintf("%v", public))
	assert.Equal(t, "plain", public.String())
	assert.Equal(t, "plain\n", fmt.Sprintln(public))
	assert.Equal(t, "plain", public.Reveal())
	assert.False(t, public.IsSensitive())

	assert.Equal(t, "[REDACTED]", fmt.Sprintf("%v", secret))
	assert.Equal(t, "[REDACTED]", secret.String())
	assert.Equal(t, "[REDACTED]\n", fmt.Sprintln(secret))
	assert.Equal(t, "hunter2", secret.Reveal())
	assert.True(t, secret.IsSensitive())

	t.Run("redacts inside a map", func(t *testing.T) {
		m := map[string]Value{"public": public, "secret": secret}
		assert.Contains(t, fmt.Sprintf("%v", m), "public:plain")
		assert.Contains(t, fmt.Sprintf("%v", m), "secret:[REDACTED]")
		assert.NotContains(t, fmt.Sprintf("%v", m), "hunter2")
	})

	t.Run("redacts inside a slice", func(t *testing.T) {
		s := []Value{public, secret}
		got := fmt.Sprintf("%v", s)
		assert.Contains(t, got, "plain")
		assert.Contains(t, got, "[REDACTED]")
		assert.NotContains(t, got, "hunter2")
	})

	t.Run("redacts inside a struct field", func(t *testing.T) {
		type wrapper struct{ V Value }
		got := fmt.Sprintf("%v", wrapper{V: secret})
		assert.Contains(t, got, "[REDACTED]")
		assert.NotContains(t, got, "hunter2")
	})

	t.Run("GoString also redacts", func(t *testing.T) {
		assert.NotContains(t, fmt.Sprintf("%#v", secret), "hunter2")
	})
}
