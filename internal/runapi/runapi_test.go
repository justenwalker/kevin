package runapi

import (
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/justenwalker/kevin/internal/config"
	"github.com/justenwalker/kevin/internal/session"
)

func TestStepSummaryOf(t *testing.T) {
	t.Run("renders needs in their kevin.cue form", func(t *testing.T) {
		got := StepSummaryOf(session.Step{
			Name:  "web",
			Needs: []config.Need{{Step: "db"}, {Setup: true, Step: "cluster"}},
		})

		assert.Equal(t, []string{"db", "setup.cluster"}, got.Needs)
	})
}
