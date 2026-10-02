package cmd

import (
	"bytes"
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	trustinstall "github.com/justenwalker/kevin/internal/trust"
)

func TestReport(t *testing.T) {
	tests := []struct {
		name   string
		result trustinstall.Result
		want   string
	}{
		{name: "an installed store", result: trustinstall.Result{Store: "macos-user", Installed: true}, want: "macos-user: installed\n"},
		{
			name:   "an installed store with a reason",
			result: trustinstall.Result{Store: "firefox", Installed: true, Reason: "2 profiles"},
			want:   "firefox: installed (2 profiles)\n",
		},
		{
			name:   "a skipped store",
			result: trustinstall.Result{Store: "linux-system", Skipped: true, Reason: "this machine has no anchor directory"},
			want:   "linux-system: skipped, this machine has no anchor directory\n",
		},
		{name: "a removed store", result: trustinstall.Result{Store: "gone"}, want: "gone: removed\n"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var buf bytes.Buffer

			report(&buf, []trustinstall.Result{tt.result})

			assert.Equal(t, tt.want, buf.String())
		})
	}
}

func TestAdvise(t *testing.T) {
	t.Run("passes another error through", func(t *testing.T) {
		boom := errors.New("boom")

		assert.Equal(t, boom, advise(boom, []trustinstall.Result{{Reason: "run: sudo x"}}))
	})

	t.Run("adds the command a store needs root for", func(t *testing.T) {
		err := advise(trustinstall.ErrNeedsRoot, []trustinstall.Result{{Store: "a"}, {Store: "b", Reason: "run: sudo cp x y"}})

		require.ErrorIs(t, err, trustinstall.ErrNeedsRoot)
		assert.ErrorContains(t, err, "run: sudo cp x y")
	})

	t.Run("keeps the error when no store has a command", func(t *testing.T) {
		assert.Equal(t, trustinstall.ErrNeedsRoot, advise(trustinstall.ErrNeedsRoot, nil))
	})
}
