//go:build e2e

package e2e

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/stretchr/testify/suite"
)

// WatchSuite covers a step's watch field: editing a watched file reruns
// the step while kevin run is still up.
type WatchSuite struct {
	e2eSuite
}

func TestWatchSuite(t *testing.T) {
	suite.Run(t, new(WatchSuite))
}

func (s *WatchSuite) TestEditRerunsTheStep() {
	project := "kevin-e2e-watch"
	dir := s.T().TempDir()
	s.cleanupProject(project)
	s.Require().NoError(os.Mkdir(filepath.Join(dir, "src"), 0o750))
	src := fmt.Sprintf(`project: %s

env: a: {
	uses:  "builtin:exec"
	watch: ["src"]
	with: up: command: ["sh", "-c", "echo built"]
}
`, strconv.Quote(project))
	s.writeCUE(dir, proxyBlock(s.T())+src)

	p := s.startKevin(dir, "-C", dir, "run")
	s.waitFor(p, stepLine("a", "ready"), defaultTimeout)

	// The watcher starts just after bring-up; keep editing until a change
	// registers rather than racing it.
	file := filepath.Join(dir, "src", "main.go")
	deadline := time.Now().Add(defaultTimeout)
	for i := 0; !p.buf.Contains("change in "); i++ {
		s.Require().True(time.Now().Before(deadline), "no rerun after an edit, output:\n%s", p.buf.String())
		s.Require().NoError(os.WriteFile(file, []byte(strconv.Itoa(i)), 0o600))
		time.Sleep(500 * time.Millisecond)
	}
	for strings.Count(p.buf.String(), stepLine("a", "ready")) < 2 {
		s.Require().True(time.Now().Before(deadline), "step never became ready again, output:\n%s", p.buf.String())
		time.Sleep(50 * time.Millisecond)
	}

	s.Require().NoError(p.cmd.Process.Signal(syscall.SIGINT))
	s.waitExit(p, defaultTimeout)
}
