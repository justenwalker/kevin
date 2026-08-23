package main

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestProbeStepIsIdempotent(t *testing.T) {
	assert.True(t, probeStep{}.Idempotent(), "a probe step creates nothing, so a rerun just checks again")
}
