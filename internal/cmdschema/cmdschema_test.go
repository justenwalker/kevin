package cmdschema_test

import (
	"testing"

	"github.com/spf13/cobra"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/justenwalker/kevin/internal/cmdschema"
)

func TestFromCommand(t *testing.T) {
	child := &cobra.Command{Use: "add <key>", Short: "add a key"}
	child.Flags().Bool("force", false, "overwrite an existing key")
	_ = child.MarkFlagRequired("force")

	parent := &cobra.Command{Use: "trust", Short: "manage trust"}
	parent.PersistentFlags().String("dir", ".", "project directory")
	parent.AddCommand(child)

	got := cmdschema.FromCommand(parent)

	assert.Equal(t, "trust", got.Name)
	require.Contains(t, got.Commands, "add")

	add := got.Commands["add"]
	assert.Equal(t, "add a key", add.Short)
	require.Len(t, add.Flags, 1)
	assert.Equal(t, cmdschema.Flag{
		Name: "force", Type: "bool", Default: "false",
		Doc: "overwrite an existing key", Required: true,
	}, add.Flags[0])

	// parent's persistent flag is its own, not inherited by add.
	require.Len(t, got.Flags, 1)
	assert.Equal(t, "dir", got.Flags[0].Name)
}
