package session

import (
	"bytes"
	"image"
	"image/png"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestAddStep(t *testing.T) {
	t.Run("keeps the order of the steps", func(t *testing.T) {
		s := NewStore()
		s.AddStep("web", "", "", "", nil, nil, false)
		s.AddStep("api", "", "", "", nil, nil, false)
		s.AddStep("db", "", "", "", nil, nil, false)
		s.AddStep("web", "", "", "", nil, nil, false) // again, which must not duplicate

		v := s.Snapshot()
		require.Len(t, v.Steps, 3)
		assert.Equal(t, []string{"web", "api", "db"},
			[]string{v.Steps[0].Name, v.Steps[1].Name, v.Steps[2].Name})
		for _, step := range v.Steps {
			assert.Equal(t, Pending, step.State)
		}
	})

	t.Run("falls back to the step name when label is empty", func(t *testing.T) {
		s := NewStore()
		s.AddStep("web", "", "", "", nil, nil, false)
		s.AddStep("api", "Public API", "", "", nil, nil, false)

		assert.Equal(t, "web", s.Snapshot().Steps[0].Label, "an empty label falls back to the step name")
		assert.Equal(t, "Public API", s.Snapshot().Steps[1].Label)
		assert.Equal(t, "api", s.Snapshot().Steps[1].Name, "Name stays the step's key regardless of Label")
	})

	t.Run("stores needs", func(t *testing.T) {
		s := NewStore()
		s.AddStep("a", "", "", "", nil, nil, false)
		s.AddStep("b", "", "", "", nil, []string{"a"}, false)

		assert.Empty(t, s.Snapshot().Steps[0].Needs)
		assert.Equal(t, []string{"a"}, s.Snapshot().Steps[1].Needs)
	})

	t.Run("copies needs so the caller cannot mutate it later", func(t *testing.T) {
		s := NewStore()
		needs := []string{"a"}
		s.AddStep("b", "", "", "", nil, needs, false)

		needs[0] = "tampered"
		assert.Equal(t, []string{"a"}, s.Snapshot().Steps[0].Needs)
	})

	t.Run("sets the provider and icon", func(t *testing.T) {
		s := NewStore()
		icon := tinyPNG(t)
		s.AddStep("web", "", "", "echo", icon, nil, false)

		step := s.Snapshot().Steps[0]
		assert.Equal(t, "echo", step.Provider)
		assert.Equal(t, icon, step.Icon)
	})

	t.Run("drops a bad icon but keeps the provider", func(t *testing.T) {
		s := NewStore()
		s.AddStep("web", "", "", "echo", []byte("not a png"), nil, false)

		step := s.Snapshot().Steps[0]
		assert.Equal(t, "echo", step.Provider)
		assert.Empty(t, step.Icon)
	})
}

func TestSetStep(t *testing.T) {
	s := NewStore()
	s.AddStep("web", "", "", "", nil, nil, false)

	s.SetStep("web", Running, "")
	assert.Equal(t, Running, s.Snapshot().Steps[0].State)

	s.SetStep("web", Failed, "boom")
	assert.Equal(t, Failed, s.Snapshot().Steps[0].State)
	assert.Equal(t, "boom", s.Snapshot().Steps[0].Message)

	// A step that no one announced still appears.
	s.SetStep("late", Ready, "")
	require.Len(t, s.Snapshot().Steps, 2)
}

func TestAddStepDetail(t *testing.T) {
	s := NewStore()
	s.AddStep("web", "", "", "", nil, nil, false)
	s.AddStepDetail("web", Detail{Value: "web.kevin.test", Href: "https://web.kevin.test", Copyable: true})
	assert.Equal(t, []Detail{{Value: "web.kevin.test", Href: "https://web.kevin.test", Copyable: true}},
		s.Snapshot().Steps[0].Details)

	// A step can publish more than one detail.
	s.AddStepDetail("web", Detail{Label: "tcp postgres", Value: "127.0.0.1:54321", Copyable: true})
	assert.Len(t, s.Snapshot().Steps[0].Details, 2)

	s.AddStepDetail("nobody", Detail{Value: "x"}) // must not panic or add a step
	assert.Len(t, s.Snapshot().Steps, 1)
}

func TestSetStepIdempotent(t *testing.T) {
	s := NewStore()
	s.AddStep("web", "", "", "", nil, nil, false)
	assert.False(t, s.Snapshot().Steps[0].Idempotent, "a step defaults to not idempotent")

	s.SetStepIdempotent("web", true)
	assert.True(t, s.Snapshot().Steps[0].Idempotent)

	s.SetStepIdempotent("nobody", true) // must not panic or add a step
	assert.Len(t, s.Snapshot().Steps, 1)
}

// tinyPNG returns a minimal valid PNG's bytes, for tests that need
// something real for validIcon to accept.
func tinyPNG(t *testing.T) []byte {
	t.Helper()
	var buf bytes.Buffer
	img := image.NewRGBA(image.Rect(0, 0, 1, 1))
	require.NoError(t, png.Encode(&buf, img))
	return buf.Bytes()
}

func TestValidIcon(t *testing.T) {
	t.Run("accepts a small PNG", func(t *testing.T) {
		icon := tinyPNG(t)
		assert.Equal(t, icon, validIcon(icon))
	})

	t.Run("rejects non-PNG bytes", func(t *testing.T) {
		assert.Empty(t, validIcon([]byte("not a png")))
	})

	t.Run("rejects an empty icon", func(t *testing.T) {
		assert.Empty(t, validIcon(nil))
	})

	t.Run("rejects an oversized icon", func(t *testing.T) {
		oversized := append(append([]byte(nil), pngMagic...), make([]byte, maxIconBytes)...)
		assert.Empty(t, validIcon(oversized))
	})
}

func TestLog(t *testing.T) {
	t.Run("derives per-step logs from the shared buffer", func(t *testing.T) {
		s := NewStore()
		s.Log("web", "stdout", "web one")
		s.Log("api", "stdout", "api one")
		s.Log("web", "stdout", "web two")

		v := s.Snapshot()
		require.Len(t, v.Logs, 3, "the shared buffer keeps every line, interleaved")
		require.Len(t, v.StepLogs["web"], 2)
		assert.Equal(t, "web one", v.StepLogs["web"][0].Text)
		assert.Equal(t, "web two", v.StepLogs["web"][1].Text)
		require.Len(t, v.StepLogs["api"], 1)
		assert.Equal(t, "api one", v.StepLogs["api"][0].Text)
	})

	t.Run("is bounded", func(t *testing.T) {
		s := NewStore()
		for i := range maxLines + 50 {
			s.Log("web", "stdout", strings.Repeat("x", 1)+string(rune('a'+i%26)))
		}

		v := s.Snapshot()
		assert.Len(t, v.Logs, maxLines, "a long run must not grow without bound")
	})
}

func TestRequestsAreBoundedAndNewestFirst(t *testing.T) {
	s := NewStore()
	s.Record(Request{Path: "/first"})
	s.Record(Request{Path: "/second"})

	v := s.Snapshot()
	assert.Equal(t, "/second", v.Requests[0].Path, "the newest request is at the top")

	for range maxRequests + 10 {
		s.Record(Request{Path: "/x"})
	}
	assert.Len(t, s.Snapshot().Requests, maxRequests)
}
