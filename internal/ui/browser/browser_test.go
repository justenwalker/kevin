package browser

import (
	"context"
	"errors"
	"os/exec"
	"runtime"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"

	"github.com/justenwalker/kevin/internal/command"
	"github.com/justenwalker/kevin/internal/command/commandtest"
)

func TestOpener(t *testing.T) {
	tests := []struct {
		goos     string
		wantName string
	}{
		{"darwin", "open"},
		{"windows", "rundll32"},
		{"linux", "xdg-open"},
		{"freebsd", "xdg-open"},
	}
	for _, tt := range tests {
		t.Run(tt.goos, func(t *testing.T) {
			name, args := opener(tt.goos, "http://127.0.0.1:8080")
			assert.Equal(t, tt.wantName, name)
			assert.Equal(t, "http://127.0.0.1:8080", args[len(args)-1])
		})
	}
}

func TestOpen(t *testing.T) {
	t.Run("starts the opener for this OS on the url", func(t *testing.T) {
		starter := commandtest.NewMockStarter(t)
		starter.EXPECT().Start(mock.Anything, mock.Anything).RunAndReturn(
			func(_ context.Context, cmd *exec.Cmd) (*command.Process, error) {
				name, _ := opener(runtime.GOOS, "")
				assert.Equal(t, name, cmd.Args[0])
				assert.Equal(t, "http://127.0.0.1:8080", cmd.Args[len(cmd.Args)-1])
				return command.NewProcess(1, func() error { return nil }), nil
			})

		require.NoError(t, Open(t.Context(), starter, "http://127.0.0.1:8080"))
	})

	t.Run("wraps a failure to start", func(t *testing.T) {
		starter := commandtest.NewMockStarter(t)
		starter.EXPECT().Start(mock.Anything, mock.Anything).Return(nil, errors.New("no opener"))

		err := Open(t.Context(), starter, "http://127.0.0.1:8080")
		require.ErrorContains(t, err, `browser: open "http://127.0.0.1:8080": no opener`)
	})
}
