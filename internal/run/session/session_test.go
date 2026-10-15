package session

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
)

func TestDeniedSince(t *testing.T) {
	start := time.Now()
	requests := []Request{
		{Time: start.Add(2 * time.Second), Denied: true, Host: "second.example.com"},
		{Time: start.Add(time.Second), Denied: true, Host: "first.example.com"},
		{Time: start.Add(time.Second), Denied: false, Host: "allowed.example.com"},
		{Time: start.Add(-time.Second), Denied: true, Host: "stale.example.com"},
	}

	t.Run("keeps denied requests at or after start, oldest first", func(t *testing.T) {
		got := DeniedSince(requests, start)
		assert.Equal(t, []Request{requests[1], requests[0]}, got)
	})

	t.Run("a request at start counts", func(t *testing.T) {
		got := DeniedSince([]Request{{Time: start, Denied: true, Host: "now.example.com"}}, start)
		assert.Len(t, got, 1)
	})

	t.Run("no denials is empty", func(t *testing.T) {
		assert.Empty(t, DeniedSince([]Request{{Time: start, Host: "ok.example.com"}}, start))
	})
}
