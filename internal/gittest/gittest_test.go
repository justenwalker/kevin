package gittest_test

import (
	"os"
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/justenwalker/kevin/internal/gittest"
)

func TestIsolate(t *testing.T) {
	t.Run("unsets repository selectors and keeps other variables", func(t *testing.T) {
		t.Setenv("GIT_DIR", "/elsewhere/.git")
		t.Setenv("GIT_WORK_TREE", "/elsewhere")
		t.Setenv("GIT_INDEX_FILE", "/elsewhere/.git/index")
		t.Setenv("KEVIN_GITTEST_KEEP", "1")

		gittest.Isolate()

		for _, name := range []string{"GIT_DIR", "GIT_WORK_TREE", "GIT_INDEX_FILE"} {
			_, ok := os.LookupEnv(name)
			assert.False(t, ok, name)
		}
		assert.Equal(t, "1", os.Getenv("KEVIN_GITTEST_KEEP"))
	})
}
