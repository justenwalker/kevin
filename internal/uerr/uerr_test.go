package uerr_test

import (
	"errors"
	"fmt"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/justenwalker/kevin/internal/uerr"
)

func TestWrap(t *testing.T) {
	t.Run("nil err returns nil", func(t *testing.T) {
		assert.NoError(t, uerr.Wrap(nil, "whatever"))
	})

	t.Run("Error passes through the wrapped chain unchanged", func(t *testing.T) {
		cause := errors.New("cause")
		err := uerr.Wrap(cause, "human: %s", "detail")
		assert.Equal(t, "cause", err.Error())
	})

	t.Run("Unwrap reaches the wrapped error", func(t *testing.T) {
		cause := errors.New("cause")
		err := uerr.Wrap(cause, "human")
		assert.ErrorIs(t, err, cause)
	})

	t.Run("Format and Message keep the template separate from the formatted text", func(t *testing.T) {
		cause := errors.New("cause")
		err := uerr.Wrap(cause, "no %s found in %s", "kevin.cue", "/tmp")
		var ue *uerr.Error
		require.ErrorAs(t, err, &ue)
		assert.Equal(t, "no %s found in %s", ue.Format())
		assert.Equal(t, "no kevin.cue found in /tmp", ue.Message())
	})

	t.Run("Args stringifies each format argument, positionally", func(t *testing.T) {
		cause := errors.New("cause")
		err := uerr.Wrap(cause, "%s needs %d retries", "step", 3)
		var ue *uerr.Error
		require.ErrorAs(t, err, &ue)
		assert.Equal(t, []string{"step", "3"}, ue.Args())
	})

	t.Run("Args is nil with no format arguments", func(t *testing.T) {
		cause := errors.New("cause")
		err := uerr.Wrap(cause, "plain text")
		var ue *uerr.Error
		require.ErrorAs(t, err, &ue)
		assert.Nil(t, ue.Args())
	})
}

func TestWrapText(t *testing.T) {
	t.Run("nil err returns nil", func(t *testing.T) {
		assert.NoError(t, uerr.WrapText(nil, "key"))
	})

	t.Run("Format returns key, Args returns args unchanged, Message renders them the same as Wrap would", func(t *testing.T) {
		cause := errors.New("cause")
		err := uerr.WrapText(cause, "no %s found in %s", "kevin.cue", "/tmp")
		var ue *uerr.Error
		require.ErrorAs(t, err, &ue)
		assert.Equal(t, "no %s found in %s", ue.Format())
		assert.Equal(t, []string{"kevin.cue", "/tmp"}, ue.Args())
		assert.Equal(t, "no kevin.cue found in /tmp", ue.Message())
	})

	t.Run("Display finds it like a normal Wrap", func(t *testing.T) {
		err := uerr.WrapText(errors.New("cause"), "friendly text")
		assert.Equal(t, "friendly text", uerr.Display(err))
	})
}

func TestDisplay(t *testing.T) {
	tests := []struct {
		name string
		err  error
		want string
	}{
		{name: "nil err", err: nil, want: ""},
		{
			name: "no human message falls back to Error",
			err:  errors.New("plain"),
			want: "plain",
		},
		{
			name: "single wrap",
			err:  uerr.Wrap(errors.New("plain"), "friendly text"),
			want: "friendly text",
		},
		{
			name: "stacked wraps join outermost first",
			err:  uerr.Wrap(uerr.Wrap(errors.New("plain"), "inner"), "outer"),
			want: "outer: inner",
		},
		{
			name: "a plain fmt.Errorf between two wraps doesn't break the walk",
			err: uerr.Wrap(
				fmt.Errorf("pkg: context: %w", uerr.Wrap(errors.New("plain"), "inner")),
				"outer",
			),
			want: "outer: inner",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, uerr.Display(tt.err))
		})
	}
}
