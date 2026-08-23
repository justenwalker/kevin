package main

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/justenwalker/kevin/plugin"
)

func TestFailSchemaDeclaresNoConfiguration(t *testing.T) {
	assert.Empty(t, failStep{}.Schema(), "a fail step takes no configuration")
}

func TestFailUpAlwaysFails(t *testing.T) {
	_, err := failStep{}.Up(t.Context(), &plugin.UpRequest{Step: "a"}, &capture{})
	require.ErrorIs(t, err, ErrRequested)
}

func TestFailStepDoesNotImplementDowner(t *testing.T) {
	_, ok := any(failStep{}).(plugin.Downer)
	assert.False(t, ok, "a fail step never has anything to tear down")
}
