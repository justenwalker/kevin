package trust

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
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
}

func TestNSSCheck(t *testing.T) {
	dirs, skip := nss{}.check()

	if skip != "" {
		assert.Empty(t, dirs)
		assert.Contains(t, skip, "certutil", "the reason must name what is missing")
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

func TestRunCmdReportsTheOutputOfAFailure(t *testing.T) {
	out, err := runCmd(t.Context(), "/bin/echo", "hello")
	require.NoError(t, err)
	assert.Equal(t, "hello\n", out)

	_, err = runCmd(t.Context(), "/bin/sh", "-c", "echo boom >&2; exit 1")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "boom", "the message of the tool must reach the user")
}

func TestProfilesReturnsDirectoriesThatHoldADatabase(t *testing.T) {
	for _, dir := range profiles() {
		assert.True(t, filepath.IsAbs(dir))
		assert.True(t, hasCertDB(dir), "a profile without a database must not appear")
	}
}
