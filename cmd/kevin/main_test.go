package main

import (
	"context"
	"os"
	"syscall"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestRunExitCodes(t *testing.T) {
	tests := []struct {
		name string
		args []string
		want int
	}{
		{
			name: "an unknown command is a usage error",
			args: []string{"kevin", "nonsense"},
			want: 2,
		},
		{
			name: "an unknown flag is a usage error",
			args: []string{"kevin", "run", "--nonsense"},
			want: 2,
		},
		{
			name: "a command failure is not a usage error",
			args: []string{"kevin", "-C", t.TempDir(), "teardown"},
			want: 1,
		},
		{
			name: "a missing kevin.cue is not a usage error",
			args: []string{"kevin", "-C", t.TempDir(), "run"},
			want: 1,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, run(t.Context(), tt.args))
		})
	}
}

func TestWatchInterrupts(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	exited := make(chan int, 1)
	watchInterrupts(cancel, func(code int) { exited <- code })

	require.NoError(t, syscall.Kill(os.Getpid(), syscall.SIGINT))
	select {
	case <-ctx.Done():
	case <-time.After(5 * time.Second):
		t.Fatal("first interrupt did not cancel the context")
	}
	select {
	case code := <-exited:
		t.Fatalf("first interrupt exited with %d", code)
	default:
	}

	require.NoError(t, syscall.Kill(os.Getpid(), syscall.SIGINT))
	select {
	case code := <-exited:
		assert.Equal(t, forceQuitCode, code)
	case <-time.After(5 * time.Second):
		t.Fatal("second interrupt did not force quit")
	}
}
