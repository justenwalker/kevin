package config

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const applySchemaSrc = `#Config: {
	image!: string
	wait: string | *"5m"
	retries?: int | *3
	http?: {method: string | *"GET"}
}`

func TestApplySchema(t *testing.T) {
	t.Run("fills in the default of a field declared without ?", func(t *testing.T) {
		got, err := ApplySchema("a.with", []byte(`{"image":"nginx"}`), []byte(applySchemaSrc))

		require.NoError(t, err)
		assert.JSONEq(t, `{"image":"nginx","wait":"5m"}`, string(got))
	})

	t.Run("does not fill in the default of an optional field", func(t *testing.T) {
		got, err := ApplySchema("a.with", []byte(`{"image":"nginx"}`), []byte(applySchemaSrc))

		require.NoError(t, err)
		assert.NotContains(t, string(got), "retries")
	})

	t.Run("fills in a default inside a block the user wrote", func(t *testing.T) {
		got, err := ApplySchema("a.with", []byte(`{"image":"nginx","http":{}}`), []byte(applySchemaSrc))

		require.NoError(t, err)
		assert.JSONEq(t, `{"image":"nginx","wait":"5m","http":{"method":"GET"}}`, string(got))
	})

	t.Run("a value the user wrote beats the default", func(t *testing.T) {
		got, err := ApplySchema("a.with", []byte(`{"image":"nginx","wait":"1s"}`), []byte(applySchemaSrc))

		require.NoError(t, err)
		assert.JSONEq(t, `{"image":"nginx","wait":"1s"}`, string(got))
	})

	t.Run("an empty block is an empty object", func(t *testing.T) {
		got, err := ApplySchema("a.with", nil, []byte(`#Config: {wait: string | *"5m"}`))

		require.NoError(t, err)
		assert.JSONEq(t, `{"wait":"5m"}`, string(got))
	})

	t.Run("a missing required field is an error", func(t *testing.T) {
		_, err := ApplySchema("a.with", []byte(`{}`), []byte(applySchemaSrc))

		require.ErrorIs(t, err, ErrInvalid)
		assert.ErrorContains(t, err, "a.with")
	})

	t.Run("a field of the wrong type is an error", func(t *testing.T) {
		_, err := ApplySchema("a.with", []byte(`{"image":3}`), []byte(applySchemaSrc))

		require.ErrorIs(t, err, ErrInvalid)
	})

	t.Run("a schema with no #Config returns the block unchanged", func(t *testing.T) {
		got, err := ApplySchema("a.with", []byte(`{"x":1}`), []byte(`other: 1`))

		require.NoError(t, err)
		assert.JSONEq(t, `{"x":1}`, string(got))
	})

	t.Run("an empty schema returns the block unchanged", func(t *testing.T) {
		got, err := ApplySchema("a.with", []byte(`{"x":1}`), nil)

		require.NoError(t, err)
		assert.JSONEq(t, `{"x":1}`, string(got))
	})

	t.Run("a schema that does not compile is an error", func(t *testing.T) {
		_, err := ApplySchema("a.with", []byte(`{}`), []byte(`#Config: {`))

		require.Error(t, err)
	})
}
