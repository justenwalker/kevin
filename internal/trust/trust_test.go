package trust

import (
	"context"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"

	"github.com/justenwalker/kevin/internal/command/commandtest"
)

func TestFileNameFor(t *testing.T) {
	tests := []struct {
		name string
		req  Request
		want string
	}{
		{
			name: "an explicit name wins",
			req:  Request{FileName: "kevin-demo", CommonName: "kevin demo CA"},
			want: "kevin-demo",
		},
		{
			name: "a common name becomes a file name",
			req:  Request{CommonName: "kevin demo CA"},
			want: "kevin-demo-ca",
		},
		{
			name: "a run of separators collapses",
			req:  Request{CommonName: "kevin   my_project  CA"},
			want: "kevin-my-project-ca",
		},
		{
			name: "a leading and trailing separator goes",
			req:  Request{CommonName: " kevin CA "},
			want: "kevin-ca",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, FileNameFor(tt.req))
		})
	}
}

func TestKeychainInstallArgs(t *testing.T) {
	k := keychain{}
	req := Request{CertPath: "/tmp/ca.crt", CommonName: "kevin demo CA"}

	user := k.installArgs(req, "/home/me/login.keychain-db")
	assert.Equal(t, []string{
		"add-trusted-cert", "-r", "trustRoot",
		"-k", "/home/me/login.keychain-db", "/tmp/ca.crt",
	}, user)
	assert.NotContains(t, user, "-d", "the user domain must not ask for root")

	req.System = true
	system := k.installArgs(req, systemKeychain)
	assert.Contains(t, system, "-d", "the machine-wide store is the admin domain")
	assert.Contains(t, system, systemKeychain)
}

func TestKeychainRemoveArgsMatchOnTheCommonName(t *testing.T) {
	args := keychain{}.removeArgs(Request{CommonName: "kevin demo CA"}, "/k")

	assert.Equal(t, []string{"delete-certificate", "-c", "kevin demo CA", "/k"}, args,
		"a removal must match this project only")
}

func TestKeychainNames(t *testing.T) {
	assert.Equal(t, "macos-user", keychain{}.name(Request{}))
	assert.Equal(t, "macos-system", keychain{}.name(Request{System: true}))
}

func TestNSSArgs(t *testing.T) {
	req := Request{CertPath: "/tmp/ca.crt", CommonName: "kevin demo CA"}

	install := nssInstallArgs(req, "/p/profile")
	assert.Equal(t, []string{
		"-A", "-d", "sql:/p/profile", "-t", "C,,", "-n", "kevin demo CA", "-i", "/tmp/ca.crt",
	}, install)

	remove := nssRemoveArgs(req, "/p/profile")
	assert.Equal(t, []string{"-D", "-d", "sql:/p/profile", "-n", "kevin demo CA"}, remove)
}

func TestHasCertDB(t *testing.T) {
	dir := t.TempDir()
	assert.False(t, hasCertDB(dir), "an empty directory is not a profile")

	require.NoError(t, os.WriteFile(filepath.Join(dir, "cert9.db"), nil, 0o600))
	assert.True(t, hasCertDB(dir))

	old := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(old, "cert8.db"), nil, 0o600))
	assert.True(t, hasCertDB(old), "an old profile still counts")
}

func TestAnchorLayoutsCoverTheCommonDistributions(t *testing.T) {
	rebuilds := make([]string, 0, len(anchorLayouts))
	for _, l := range anchorLayouts {
		assert.True(t, filepath.IsAbs(l.dir), "an anchor directory must be absolute")
		rebuilds = append(rebuilds, l.rebuild)
	}
	assert.Contains(t, rebuilds, "update-ca-certificates", "Debian")
	assert.Contains(t, rebuilds, "update-ca-trust", "Fedora and RHEL")
}

func TestQuoteMakesACommandThatAUserCanPaste(t *testing.T) {
	assert.Equal(t, `security delete-certificate -c "kevin demo CA" /k`,
		quote("security", "delete-certificate", "-c", "kevin demo CA", "/k"))
	assert.Equal(t, "certutil -D -d sql:/p", quote("certutil", "-D", "-d", "sql:/p"))
}

func TestStoresFollowTheRequest(t *testing.T) {
	withFirefox := stores(Request{Firefox: true})
	withoutFirefox := stores(Request{Firefox: false})

	assert.Len(t, withFirefox, len(withoutFirefox)+1, "firefox adds one store")
	assert.NotEmpty(t, withoutFirefox, "this machine must have a system store")
}

func TestPlural(t *testing.T) {
	assert.Equal(t, "1 profile", plural(1))
	assert.Equal(t, "0 profiles", plural(0))
	assert.Equal(t, "3 profiles", plural(3))
}

// The tests below stop at the boundary of the exec call. Running the real
// command would change the trust store of the machine that runs the test.

func TestKeychainSystemNeedsRootAndReportsTheCommand(t *testing.T) {
	if isRoot() {
		t.Skip("this test needs a process that is not root")
	}

	req := Request{CertPath: "/tmp/ca.crt", CommonName: "kevin demo CA", System: true}

	result, err := keychain{}.install(t.Context(), req)
	require.ErrorIs(t, err, ErrNeedsRoot)
	assert.False(t, result.Installed)
	assert.Contains(t, result.Reason, "sudo", "the user must see the command to run")
	assert.Contains(t, result.Reason, systemKeychain)

	result, err = keychain{}.remove(t.Context(), req)
	require.ErrorIs(t, err, ErrNeedsRoot)
	assert.Contains(t, result.Reason, "delete-certificate")
}

func TestKeychainStatusReportsNotInstalledForAnUnknownName(t *testing.T) {
	// A CommonName that has never been added to any keychain. security
	// (or its absence on a non-darwin test runner) reports it as absent
	// either way, so this stays deterministic without touching a real
	// keychain.
	result, err := keychain{}.status(t.Context(), Request{CommonName: "kevin doctor test CA, never installed"})
	require.NoError(t, err)
	assert.False(t, result.Installed)
}

func TestKeychainTarget(t *testing.T) {
	system, err := keychain{}.target(Request{System: true})
	require.NoError(t, err)
	assert.Equal(t, systemKeychain, system)

	user, err := keychain{}.target(Request{})
	require.NoError(t, err)
	assert.Contains(t, user, "login.keychain-db")
	assert.True(t, filepath.IsAbs(user))
}

func TestAnchorDir(t *testing.T) {
	t.Run("skips a machine without an anchor directory", func(t *testing.T) {
		if _, ok := (anchorDir{}).layout(); ok {
			t.Skip("this machine has an anchor directory")
		}

		req := Request{CertPath: "/tmp/ca.crt", CommonName: "kevin demo CA"}

		result, err := anchorDir{}.install(t.Context(), req)
		require.NoError(t, err, "an absent store is a skip, not a failure")
		assert.True(t, result.Skipped)

		result, err = anchorDir{}.remove(t.Context(), req)
		require.NoError(t, err)
		assert.True(t, result.Skipped)
	})

	t.Run("needs root and reports the command", func(t *testing.T) {
		if isRoot() {
			t.Skip("this test needs a process that is not root")
		}

		// Point the layout at a directory that exists, so that the code
		// reaches the check for root instead of the skip.
		dir := t.TempDir()
		restore := anchorLayouts
		anchorLayouts = []anchorLayout{{dir: dir, suffix: anchorSuffix, rebuild: "update-ca-certificates"}}
		t.Cleanup(func() { anchorLayouts = restore })

		result, err := anchorDir{}.install(t.Context(), Request{
			CertPath: "/tmp/ca.crt", CommonName: "kevin demo CA", FileName: "kevin-demo",
		})
		require.ErrorIs(t, err, ErrNeedsRoot)
		assert.Contains(t, result.Reason, "sudo cp")
		assert.Contains(t, result.Reason, filepath.Join(dir, "kevin-demo.crt"))
		assert.Contains(t, result.Reason, "update-ca-certificates")

		// Nothing was written, because the process is not root.
		entries, err := os.ReadDir(dir)
		require.NoError(t, err)
		assert.Empty(t, entries)
	})

	t.Run("remove skips an authority that is absent", func(t *testing.T) {
		dir := t.TempDir()
		restore := anchorLayouts
		anchorLayouts = []anchorLayout{{dir: dir, suffix: anchorSuffix, rebuild: "update-ca-certificates"}}
		t.Cleanup(func() { anchorLayouts = restore })

		result, err := anchorDir{}.remove(t.Context(), Request{CommonName: "kevin demo CA"})
		require.NoError(t, err)
		assert.True(t, result.Skipped)
		assert.Contains(t, result.Reason, "does not hold")
	})

	t.Run("remove needs root and reports the command", func(t *testing.T) {
		if isRoot() {
			t.Skip("this test needs a process that is not root")
		}

		dir := t.TempDir()
		restore := anchorLayouts
		anchorLayouts = []anchorLayout{{dir: dir, suffix: anchorSuffix, rebuild: "update-ca-certificates"}}
		t.Cleanup(func() { anchorLayouts = restore })

		target := filepath.Join(dir, "kevin-demo.crt")
		require.NoError(t, os.WriteFile(target, []byte("pem"), 0o600))

		result, err := anchorDir{}.remove(t.Context(), Request{CommonName: "kevin demo CA", FileName: "kevin-demo"})
		require.ErrorIs(t, err, ErrNeedsRoot)
		assert.Contains(t, result.Reason, "sudo rm "+target)
		assert.FileExists(t, target)
	})

	t.Run("status reports not-installed then installed, and writes nothing", func(t *testing.T) {
		dir := t.TempDir()
		restore := anchorLayouts
		anchorLayouts = []anchorLayout{{dir: dir, suffix: anchorSuffix, rebuild: "update-ca-certificates"}}
		t.Cleanup(func() { anchorLayouts = restore })

		req := Request{CommonName: "kevin demo CA", FileName: "kevin-demo"}

		result, err := anchorDir{}.status(t.Context(), req)
		require.NoError(t, err)
		assert.False(t, result.Installed)

		require.NoError(t, os.WriteFile(filepath.Join(dir, "kevin-demo.crt"), []byte("pem"), 0o600))

		result, err = anchorDir{}.status(t.Context(), req)
		require.NoError(t, err)
		assert.True(t, result.Installed)

		entries, err := os.ReadDir(dir)
		require.NoError(t, err)
		assert.Len(t, entries, 1, "status must not write anything of its own")
	})
}

func TestNSSCheck(t *testing.T) {
	dirs, skip := nss{}.check()

	if skip != "" {
		assert.Empty(t, dirs)
		// check has two legitimate skip reasons: certutil is missing, or
		// certutil is present but this machine has no Firefox/fork profile.
		assert.True(t,
			strings.Contains(skip, "certutil") || strings.Contains(skip, "no Firefox profile"),
			"the reason must name what is missing, got %q", skip)
		return
	}
	assert.NotEmpty(t, dirs, "a store that is not skipped must have a profile")
}

func TestNSSSkipsWhenCertutilIsAbsent(t *testing.T) {
	if _, skip := (nss{}).check(); skip == "" {
		t.Skip("this machine has certutil and a Firefox profile")
	}

	req := Request{CertPath: "/tmp/ca.crt", CommonName: "kevin demo CA"}

	result, err := nss{}.install(t.Context(), req)
	require.NoError(t, err, "an absent certutil is a skip, not a failure")
	assert.True(t, result.Skipped)

	result, err = nss{}.remove(t.Context(), req)
	require.NoError(t, err)
	assert.True(t, result.Skipped)
}

func TestNSSStatusSkipsWhenCertutilIsAbsent(t *testing.T) {
	if _, skip := (nss{}).check(); skip == "" {
		t.Skip("this machine has certutil and a Firefox profile")
	}

	result, err := nss{}.status(t.Context(), Request{CommonName: "kevin demo CA"})
	require.NoError(t, err)
	assert.True(t, result.Skipped)
}

func TestProfileGlobsPointAtTheHome(t *testing.T) {
	globs := profileGlobs()
	require.NotEmpty(t, globs)

	home, err := os.UserHomeDir()
	require.NoError(t, err)
	for _, g := range globs {
		assert.True(t, filepath.IsAbs(g))
		assert.Contains(t, g, home)
	}
}

func TestAbsentFromKeychain(t *testing.T) {
	// A removal must be idempotent. These are the words that security uses
	// when the keychain holds no such certificate.
	tests := []struct {
		name string
		out  string
		want bool
	}{
		{
			name: "the wording of delete-certificate",
			out:  `Unable to delete certificate matching "kevin demo CA"`,
			want: true,
		},
		{
			name: "the wording of a missing item",
			out:  "The specified item could not be found in the keychain.",
			want: true,
		},
		{
			name: "a real failure",
			out:  "SecKeychainDelete: User interaction is not allowed.",
			want: false,
		},
		{name: "no output at all", out: "", want: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, absentFromKeychain(tt.out))
		})
	}
}

func TestAlreadyInDatabase(t *testing.T) {
	// An install must be idempotent. This is the wording certutil uses when
	// a certificate with this nickname is already in the database.
	tests := []struct {
		name string
		out  string
		want bool
	}{
		{
			name: "the wording of a duplicate nickname",
			out:  "certutil: unable to rename certificate: A certificate with the same nickname already exists.",
			want: true,
		},
		{
			name: "a real failure",
			out:  "certutil: could not find certificate: SEC_ERROR_BAD_DATABASE",
			want: false,
		},
		{name: "no output at all", out: "", want: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, alreadyInDatabase(tt.out))
		})
	}
}

func TestInstallAndRemoveStopAtTheStoreThatNeedsRoot(t *testing.T) {
	if isRoot() {
		t.Skip("this test needs a process that is not root")
	}

	// System and no Firefox. The machine-wide store checks for root and
	// returns before it runs any command, thus this test changes nothing.
	req := Request{CertPath: "/tmp/ca.crt", CommonName: "kevin demo CA", System: true}

	for _, tc := range []struct {
		name string
		fn   func() ([]Result, error)
	}{
		{name: "install", fn: func() ([]Result, error) { return Install(t.Context(), req) }},
		{name: "remove", fn: func() ([]Result, error) { return Remove(t.Context(), req) }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			results, err := tc.fn()

			require.ErrorIs(t, err, ErrNeedsRoot)
			require.NotEmpty(t, results, "the failing store must still be reported")

			last := results[len(results)-1]
			assert.False(t, last.Installed)
			assert.Contains(t, last.Reason, "sudo", "the user must see the command to run")
		})
	}
}

func TestStatusReportsEveryStoreWithoutWriting(t *testing.T) {
	req := Request{CommonName: "kevin doctor test CA, never installed", Firefox: true}

	results, err := Status(t.Context(), req)
	require.NoError(t, err, "status must never need root or fail on a missing store")

	for _, r := range results {
		assert.False(t, r.Installed, "an unknown CommonName must never read as installed")
	}
}

func TestRunCmdReportsTheOutputOfAFailure(t *testing.T) {
	t.Run("returns the combined output", func(t *testing.T) {
		runner := commandtest.NewMockRunner(t)
		runner.EXPECT().Run(mock.Anything, mock.Anything).RunAndReturn(
			func(_ context.Context, cmd *exec.Cmd) error {
				assert.Equal(t, []string{"echo", "hello"}, cmd.Args)
				_, err := io.WriteString(cmd.Stdout, "hello\n")
				return err
			})

		out, err := runCmd(t.Context(), Request{Runner: runner}, "echo", "hello")
		require.NoError(t, err)
		assert.Equal(t, "hello\n", out)
	})

	t.Run("puts the output of the tool in the error", func(t *testing.T) {
		runner := commandtest.NewMockRunner(t)
		runner.EXPECT().Run(mock.Anything, mock.Anything).RunAndReturn(
			func(_ context.Context, cmd *exec.Cmd) error {
				_, _ = io.WriteString(cmd.Stderr, "boom\n")
				return errors.New("exit status 1")
			})

		_, err := runCmd(t.Context(), Request{Runner: runner}, "sh")
		require.Error(t, err)
		assert.Contains(t, err.Error(), "boom", "the message of the tool must reach the user")
	})

	t.Run("runs real processes without a Runner", func(t *testing.T) {
		out, err := runCmd(t.Context(), Request{}, "/bin/echo", "hello")
		require.NoError(t, err)
		assert.Equal(t, "hello\n", out)
	})
}

func TestKeychainRunsSecurity(t *testing.T) {
	req := Request{CertPath: "/tmp/ca.crt", CommonName: "kevin demo CA"}

	t.Run("install adds the certificate", func(t *testing.T) {
		runner := commandtest.NewMockRunner(t)
		runner.EXPECT().Run(mock.Anything, mock.Anything).RunAndReturn(
			func(_ context.Context, cmd *exec.Cmd) error {
				assert.Equal(t, SecurityBinary, cmd.Args[0])
				assert.Contains(t, cmd.Args, "add-trusted-cert")
				return nil
			})
		req := req
		req.Runner = runner

		result, err := keychain{}.install(t.Context(), req)
		require.NoError(t, err)
		assert.True(t, result.Installed)
	})

	t.Run("install reports the command to run by hand", func(t *testing.T) {
		runner := commandtest.NewMockRunner(t)
		runner.EXPECT().Run(mock.Anything, mock.Anything).Return(errors.New("exit status 1"))
		req := req
		req.Runner = runner

		result, err := keychain{}.install(t.Context(), req)
		require.Error(t, err)
		assert.Contains(t, result.Reason, "add-trusted-cert")
	})

	t.Run("remove skips a certificate the keychain does not hold", func(t *testing.T) {
		runner := commandtest.NewMockRunner(t)
		runner.EXPECT().Run(mock.Anything, mock.Anything).RunAndReturn(
			func(_ context.Context, cmd *exec.Cmd) error {
				_, _ = io.WriteString(cmd.Stderr, "SecKeychainSearchCopyNext: The specified item could not be found in the keychain.")
				return errors.New("exit status 44")
			})
		req := req
		req.Runner = runner

		result, err := keychain{}.remove(t.Context(), req)
		require.NoError(t, err)
		assert.True(t, result.Skipped)
	})

	t.Run("status reads the certificate as installed when security finds it", func(t *testing.T) {
		runner := commandtest.NewMockRunner(t)
		runner.EXPECT().Run(mock.Anything, mock.Anything).Return(nil)
		req := req
		req.Runner = runner

		result, err := keychain{}.status(t.Context(), req)
		require.NoError(t, err)
		assert.True(t, result.Installed)
	})
}

func TestProfilesReturnsDirectoriesThatHoldADatabase(t *testing.T) {
	for _, dir := range profiles() {
		assert.True(t, filepath.IsAbs(dir))
		assert.True(t, hasCertDB(dir), "a profile without a database must not appear")
	}
}

// fakeFirefox puts a certutil on PATH and one profile with a database under
// a temporary home, and returns the profile directory.
func fakeFirefox(t *testing.T) string {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)

	bin := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(bin, CertutilBinary), []byte("#!/bin/sh\n"), 0o755))
	t.Setenv("PATH", bin)

	profile := filepath.Join(filepath.Dir(profileGlobs()[0]), "p1")
	require.NoError(t, os.MkdirAll(profile, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(profile, "cert9.db"), nil, 0o600))
	return profile
}

func TestNSSRunsCertutil(t *testing.T) {
	req := Request{CertPath: "/tmp/ca.crt", CommonName: "kevin demo CA"}

	t.Run("skips a machine without a profile", func(t *testing.T) {
		fakeFirefox(t)
		t.Setenv("HOME", t.TempDir())

		for _, run := range []func(context.Context, Request) (Result, error){nss{}.install, nss{}.status, nss{}.remove} {
			result, err := run(t.Context(), req)
			require.NoError(t, err)
			assert.True(t, result.Skipped)
			assert.Contains(t, result.Reason, "no Firefox profile")
		}
	})

	t.Run("install adds the authority to each profile", func(t *testing.T) {
		profile := fakeFirefox(t)
		runner := commandtest.NewMockRunner(t)
		runner.EXPECT().Run(mock.Anything, mock.Anything).RunAndReturn(
			func(_ context.Context, cmd *exec.Cmd) error {
				assert.Equal(t, nssInstallArgs(req, profile), cmd.Args[1:])
				return nil
			})
		req := req
		req.Runner = runner

		result, err := nss{}.install(t.Context(), req)
		require.NoError(t, err)
		assert.True(t, result.Installed)
		assert.Equal(t, "1 profile", result.Reason)
	})

	t.Run("install accepts an authority the database already holds", func(t *testing.T) {
		fakeFirefox(t)
		runner := commandtest.NewMockRunner(t)
		runner.EXPECT().Run(mock.Anything, mock.Anything).RunAndReturn(
			func(_ context.Context, cmd *exec.Cmd) error {
				_, _ = io.WriteString(cmd.Stderr, "certutil: could not add certificate: The nickname already exists.")
				return errors.New("exit status 255")
			})
		req := req
		req.Runner = runner

		result, err := nss{}.install(t.Context(), req)
		require.NoError(t, err)
		assert.True(t, result.Installed)
	})

	t.Run("install reports the command to run by hand", func(t *testing.T) {
		fakeFirefox(t)
		runner := commandtest.NewMockRunner(t)
		runner.EXPECT().Run(mock.Anything, mock.Anything).Return(errors.New("exit status 255"))
		req := req
		req.Runner = runner

		result, err := nss{}.install(t.Context(), req)
		require.Error(t, err)
		assert.False(t, result.Installed)
		assert.Contains(t, result.Reason, "certutil -A")
	})

	t.Run("status reports a profile that lacks the authority", func(t *testing.T) {
		fakeFirefox(t)
		runner := commandtest.NewMockRunner(t)
		runner.EXPECT().Run(mock.Anything, mock.Anything).Return(errors.New("exit status 255"))
		req := req
		req.Runner = runner

		result, err := nss{}.status(t.Context(), req)
		require.NoError(t, err)
		assert.False(t, result.Installed)
		assert.Equal(t, "missing in 1 of 1 profile", result.Reason)
	})

	t.Run("status reads the authority as installed when every profile holds it", func(t *testing.T) {
		fakeFirefox(t)
		runner := commandtest.NewMockRunner(t)
		runner.EXPECT().Run(mock.Anything, mock.Anything).Return(nil)
		req := req
		req.Runner = runner

		result, err := nss{}.status(t.Context(), req)
		require.NoError(t, err)
		assert.True(t, result.Installed)
	})

	t.Run("remove ignores a profile that does not hold the authority", func(t *testing.T) {
		profile := fakeFirefox(t)
		runner := commandtest.NewMockRunner(t)
		runner.EXPECT().Run(mock.Anything, mock.Anything).RunAndReturn(
			func(_ context.Context, cmd *exec.Cmd) error {
				assert.Equal(t, nssRemoveArgs(req, profile), cmd.Args[1:])
				return errors.New("exit status 255")
			})
		req := req
		req.Runner = runner

		result, err := nss{}.remove(t.Context(), req)
		require.NoError(t, err)
		assert.False(t, result.Skipped)
		assert.Equal(t, "1 profile", result.Reason)
	})
}
