package browser

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestCommand(t *testing.T) {
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
			name, args := command(tt.goos, "http://127.0.0.1:8080")
			assert.Equal(t, tt.wantName, name)
			assert.Equal(t, "http://127.0.0.1:8080", args[len(args)-1])
		})
	}
}
