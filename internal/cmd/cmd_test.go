package cmd_test

import (
	"errors"
	"io"
	"os"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/justenwalker/kevin/internal/cmd"
	"github.com/justenwalker/kevin/internal/config"
	"github.com/justenwalker/kevin/internal/pkgtrust"
)

// captureStdout runs fn and returns everything that fn writes to os.Stdout.
func captureStdout(t *testing.T, fn func()) string {
	t.Helper()
	r, w, err := os.Pipe()
	require.NoError(t, err)

	original := os.Stdout
	os.Stdout = w
	defer func() { os.Stdout = original }()

	fn()

	require.NoError(t, w.Close())
	out, err := io.ReadAll(r)
	require.NoError(t, err)
	return string(out)
}

func TestRun(t *testing.T) {
	t.Run("classifies command failures", func(t *testing.T) {
		tests := []struct {
			name           string
			args           []string
			wantUsageError bool
			wantContains   string
		}{
			{name: "an unknown command", args: []string{"nonsense"}, wantUsageError: true},
			{name: "an unknown flag", args: []string{"run", "--nonsense"}, wantUsageError: true, wantContains: "nonsense"},
			{name: "too many arguments", args: []string{"teardown", "extra"}, wantUsageError: true},
		}
		for _, tt := range tests {
			t.Run(tt.name, func(t *testing.T) {
				err := cmd.Run(t.Context(), tt.args)
				require.Error(t, err)

				var cmdErr *cmd.CommandError
				if tt.wantUsageError {
					require.ErrorAs(t, err, &cmdErr, "must be a usage error")
				} else {
					assert.NotErrorAs(t, err, &cmdErr, "must not be a usage error")
				}
				if tt.wantContains != "" {
					assert.Contains(t, err.Error(), tt.wantContains)
				}
			})
		}
	})

	t.Run("a usage error carries the command for the usage text", func(t *testing.T) {
		err := cmd.Run(t.Context(), []string{"nonsense"})

		var cmdErr *cmd.CommandError
		require.ErrorAs(t, err, &cmdErr)
		assert.NotNil(t, cmdErr.Cmd, "the caller needs the command to print the usage text")
		assert.ErrorIs(t, cmdErr, cmdErr.Err, "Unwrap must expose the cause")
	})

	t.Run("passes the project directory to the engine", func(t *testing.T) {
		tests := []struct {
			name string
			args []string
		}{
			{name: "run", args: []string{"-C", t.TempDir(), "run"}},
			{name: "setup", args: []string{"-C", t.TempDir(), "setup"}},
			{name: "teardown", args: []string{"-C", t.TempDir(), "teardown"}},
		}

		for _, tt := range tests {
			t.Run(tt.name, func(t *testing.T) {
				err := cmd.Run(t.Context(), tt.args)

				// The directory has no kevin.cue, thus the engine stops there.
				// That proves the flag reached it.
				require.ErrorIs(t, err, config.ErrNotFound)

				var cmdErr *cmd.CommandError
				assert.NotErrorAs(t, err, &cmdErr, "a missing file is not a usage error")
			})
		}
	})

	t.Run("reports a command failure without the usage text", func(t *testing.T) {
		// The teardown body runs, then fails on the missing file. That is a
		// failure of the command, not a usage error.
		err := cmd.Run(t.Context(), []string{"-C", t.TempDir(), "teardown"})
		require.ErrorIs(t, err, config.ErrNotFound)

		var cmdErr *cmd.CommandError
		assert.NotErrorAs(t, err, &cmdErr, "a command failure must not be a CommandError")
	})

	t.Run("KEVIN_ENV selects a named environment", func(t *testing.T) {
		t.Setenv("KEVIN_ENV", "staging")

		// No staging.kevin.cue in dir, so the engine's search for it (not
		// the default one) must be what fails. That proves KEVIN_ENV reached
		// the engine.
		err := cmd.Run(t.Context(), []string{"-C", t.TempDir(), "teardown"})
		require.ErrorIs(t, err, config.ErrNotFound)
	})

	t.Run("--env overrides KEVIN_ENV", func(t *testing.T) {
		t.Setenv("KEVIN_ENV", "staging")
		dir := t.TempDir()
		require.NoError(t, os.WriteFile(dir+"/kevin.cue", []byte(`plugins: {}`), 0o600))

		// The unnamed kevin.cue exists but staging.kevin.cue does not: with
		// KEVIN_ENV alone this would fail like the case above. Passing
		// --env "" must select the unnamed file instead and get past Load.
		err := cmd.Run(t.Context(), []string{"-C", dir, "--env", "", "teardown"})
		require.NotErrorIs(t, err, config.ErrNotFound, "--env must override KEVIN_ENV")
	})

	t.Run("plugin list prints every qualified step type", func(t *testing.T) {
		var runErr error
		out := captureStdout(t, func() {
			runErr = cmd.Run(t.Context(), []string{"plugin", "list"})
		})
		require.NoError(t, runErr, "plugin list must succeed")

		assert.Contains(t, out, "builtin:container")
		assert.Contains(t, out, "builtin:kind")
	})

	t.Run("plugin run reports an unknown plugin", func(t *testing.T) {
		err := cmd.Run(t.Context(), []string{"plugin", "run", "nonsense"})
		require.Error(t, err, "an unknown provider name must fail")

		var cmdErr *cmd.CommandError
		assert.NotErrorAs(t, err, &cmdErr, "an unknown provider is a command failure, not a usage error")
		assert.Contains(t, err.Error(), "builtin", "the error must name what is available")
	})

	t.Run("plugin pack builds a package from a directory", func(t *testing.T) {
		srcDir := t.TempDir()
		require.NoError(t, os.WriteFile(srcDir+"/acme", []byte("x"), 0o755))
		outPath := t.TempDir() + "/acme.tar.gz"

		var runErr error
		out := captureStdout(t, func() {
			runErr = cmd.Run(t.Context(), []string{
				"plugin", "pack", srcDir,
				"-o", outPath,
				"--name", "acme",
				"--version", "1.0.0",
				"--entrypoint", "acme",
			})
		})
		require.NoError(t, runErr)
		assert.Contains(t, out, "acme 1.0.0")
		assert.FileExists(t, outPath)
	})

	t.Run("plugin pack requires --output", func(t *testing.T) {
		err := cmd.Run(t.Context(), []string{"plugin", "pack", t.TempDir()})
		require.Error(t, err)

		var cmdErr *cmd.CommandError
		require.ErrorAs(t, err, &cmdErr, "a missing required flag is a usage error")
	})

	t.Run("plugin push reports a bad reference", func(t *testing.T) {
		tarPath := t.TempDir() + "/pkg.tar.gz"
		require.NoError(t, os.WriteFile(tarPath, []byte("x"), 0o600))

		err := cmd.Run(t.Context(), []string{"plugin", "push", tarPath, "not a valid ref!!"})
		require.Error(t, err, "a bad reference must fail")

		var cmdErr *cmd.CommandError
		assert.NotErrorAs(t, err, &cmdErr, "a bad reference is a command failure, not a usage error")
	})

	t.Run("plugin trust add/list/remove round-trips a key", func(t *testing.T) {
		t.Setenv("HOME", t.TempDir())

		pubDir := t.TempDir()
		pubPath := pubDir + "/signer.pub"
		// A real minisign public key (42 decoded bytes: "Ed" + 8-byte key id
		// + 32-byte Ed25519 key) - content doesn't need to correspond to a
		// real secret key for add/list/remove, only for Verify.
		require.NoError(t, os.WriteFile(pubPath,
			[]byte("untrusted comment: test key\nRWQf6LRCGA9i53mlYecO4IzT51TGPpvWucNSCh1CBM0QTaLn73Y7GFO3\n"),
			0o600))

		var runErr error
		addOut := captureStdout(t, func() {
			runErr = cmd.Run(t.Context(), []string{"plugin", "trust", "add", pubPath})
		})
		require.NoError(t, runErr)
		id := strings.TrimSpace(addOut)
		assert.NotEmpty(t, id)

		listOut := captureStdout(t, func() {
			runErr = cmd.Run(t.Context(), []string{"plugin", "trust", "list"})
		})
		require.NoError(t, runErr)
		assert.Contains(t, listOut, id)

		runErr = cmd.Run(t.Context(), []string{"plugin", "trust", "remove", id})
		require.NoError(t, runErr)

		listOut = captureStdout(t, func() {
			runErr = cmd.Run(t.Context(), []string{"plugin", "trust", "list"})
		})
		require.NoError(t, runErr)
		assert.Empty(t, strings.TrimSpace(listOut))
	})

	t.Run("plugin trust add rejects a bad key", func(t *testing.T) {
		t.Setenv("HOME", t.TempDir())
		badPath := t.TempDir() + "/not-a-key.pub"
		require.NoError(t, os.WriteFile(badPath, []byte("not a minisign key"), 0o600))

		err := cmd.Run(t.Context(), []string{"plugin", "trust", "add", badPath})
		require.ErrorIs(t, err, pkgtrust.ErrBadKey)
	})

	t.Run("plugin trust remove reports an unknown key", func(t *testing.T) {
		t.Setenv("HOME", t.TempDir())
		err := cmd.Run(t.Context(), []string{"plugin", "trust", "remove", "deadbeefdeadbeef"})
		require.ErrorIs(t, err, pkgtrust.ErrUnknownKeyID)
	})
}

func TestValidateCommand(t *testing.T) {
	t.Run("reports a missing file", func(t *testing.T) {
		err := cmd.Run(t.Context(), []string{"-C", t.TempDir(), "validate"})
		require.ErrorIs(t, err, config.ErrNotFound)

		var cmdErr *cmd.CommandError
		assert.NotErrorAs(t, err, &cmdErr, "a missing file is not a usage error")
	})

	t.Run("reports a step that names an undeclared plugin", func(t *testing.T) {
		dir := t.TempDir()
		require.NoError(t, os.WriteFile(dir+"/kevin.cue", []byte(
			`project: "x"
env: { web: { uses: "unknown:container" } }`,
		), 0o600))

		err := cmd.Run(t.Context(), []string{"-C", dir, "validate"})
		require.ErrorIs(t, err, config.ErrUnknownPlugin)
	})

	t.Run("prints the step counts on success", func(t *testing.T) {
		dir := t.TempDir()
		require.NoError(t, os.WriteFile(dir+"/kevin.cue", []byte(`project: "x"`), 0o600))

		var runErr error
		out := captureStdout(t, func() {
			runErr = cmd.Run(t.Context(), []string{"-C", dir, "validate"})
		})
		require.NoError(t, runErr)
		assert.Contains(t, out, "x: 0 setup step(s), 0 env step(s)")
	})

	t.Run("--tag reaches the engine", func(t *testing.T) {
		dir := t.TempDir()
		require.NoError(t, os.WriteFile(dir+"/kevin.cue", []byte("package kevin\n\nproject: \"x\"\n"), 0o600))

		// No @tag field named "bogus" anywhere in the file, so CUE's own
		// "tag not used" error is what fails. That proves --tag reached
		// config.Load through opts.tags.
		err := cmd.Run(t.Context(), []string{"-C", dir, "--tag", "bogus=1", "validate"})
		require.Error(t, err)
		assert.Contains(t, err.Error(), "bogus")
	})
}

func TestInitCommand(t *testing.T) {
	t.Run("reports a missing file", func(t *testing.T) {
		err := cmd.Run(t.Context(), []string{"-C", t.TempDir(), "init"})
		require.ErrorIs(t, err, config.ErrNotFound)

		var cmdErr *cmd.CommandError
		assert.NotErrorAs(t, err, &cmdErr, "a missing file is not a usage error")
	})

	t.Run("reports a step that names an undeclared plugin", func(t *testing.T) {
		dir := t.TempDir()
		require.NoError(t, os.WriteFile(dir+"/kevin.cue", []byte(
			`project: "x"
env: { web: { uses: "unknown:container" } }`,
		), 0o600))

		err := cmd.Run(t.Context(), []string{"-C", dir, "init"})
		require.ErrorIs(t, err, config.ErrUnknownPlugin)
	})

	t.Run("prints every non-builtin plugin a step uses, without starting it", func(t *testing.T) {
		dir := t.TempDir()
		// cmd: names a local binary kevin never launches here, so a
		// nonexistent path is fine - init only resolves the spec.
		require.NoError(t, os.WriteFile(dir+"/kevin.cue", []byte(
			`project: "x"
plugins: { acme: { cmd: "/nonexistent/acme" } }
env: { web: { uses: "acme:widget" }, sidecar: { uses: "builtin:wait" } }`,
		), 0o600))

		var runErr error
		out := captureStdout(t, func() {
			runErr = cmd.Run(t.Context(), []string{"-C", dir, "init"})
		})
		require.NoError(t, runErr)
		assert.Equal(t, "acme\n", out, "builtin must not be listed, acme's process must not start")
	})
}

func TestCommandErrorUnwraps(t *testing.T) {
	sentinel := errors.New("boom")
	err := &cmd.CommandError{Err: sentinel}

	assert.Equal(t, "boom", err.Error())
	assert.ErrorIs(t, err, sentinel)
}
