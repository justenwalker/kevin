package cmd

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestExecWith(t *testing.T) {
	err := execWith([]string{"kevin-no-such-binary"})

	require.ErrorContains(t, err, "do:")
}
