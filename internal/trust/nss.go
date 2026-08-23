package trust

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
	"strings"
)

// nss is the certificate database of Firefox. Firefox ignores the trust store
// of the operating system and keeps its own database for each profile.
type nss struct{}

// CertutilBinary edits an NSS database. It ships with the nss package, and a
// machine without it is skipped.
const CertutilBinary = "certutil"

func (nss) name() string { return "firefox" }

// profileGlobs are the places where Firefox keeps a profile.
func profileGlobs() []string {
	home, err := os.UserHomeDir()
	if err != nil {
		return nil
	}
	switch runtime.GOOS {
	case "darwin":
		return []string{
			filepath.Join(home, "Library", "Application Support", "Firefox", "Profiles", "*"),
		}
	default:
		return []string{
			filepath.Join(home, ".mozilla", "firefox", "*"),
			filepath.Join(home, "snap", "firefox", "common", ".mozilla", "firefox", "*"),
		}
	}
}

// profiles returns every Firefox profile that holds a certificate database.
func profiles() []string {
	var found []string
	for _, glob := range profileGlobs() {
		matches, err := filepath.Glob(glob)
		if err != nil {
			continue
		}
		for _, dir := range matches {
			if hasCertDB(dir) {
				found = append(found, dir)
			}
		}
	}
	sort.Strings(found)
	return found
}

// hasCertDB reports whether a directory holds an NSS database. Firefox writes
// cert9.db, and an old profile writes cert8.db.
func hasCertDB(dir string) bool {
	for _, name := range []string{"cert9.db", "cert8.db"} {
		if _, err := os.Stat(filepath.Join(dir, name)); err == nil {
			return true
		}
	}
	return false
}

// nssInstallArgs builds the arguments that add an authority to one profile.
func nssInstallArgs(req Request, profile string) []string {
	return []string{
		"-A",
		"-d", "sql:" + profile,
		"-t", "C,,",
		"-n", req.CommonName,
		"-i", req.CertPath,
	}
}

// nssRemoveArgs builds the arguments that delete an authority from one profile.
func nssRemoveArgs(req Request, profile string) []string {
	return []string{"-D", "-d", "sql:" + profile, "-n", req.CommonName}
}

func (n nss) install(ctx context.Context, req Request) (Result, error) {
	result := Result{Store: n.name()}

	dirs, skip := n.check()
	if skip != "" {
		result.Skipped = true
		result.Reason = skip
		return result, nil
	}

	for _, profile := range dirs {
		out, err := runCmd(ctx, CertutilBinary, nssInstallArgs(req, profile)...)
		if err != nil && !alreadyInDatabase(out) {
			result.Reason = "run: " + quote(CertutilBinary, nssInstallArgs(req, profile)...)
			return result, err
		}
	}

	result.Installed = true
	result.Reason = plural(len(dirs))
	return result, nil
}

// alreadyInDatabase reports whether certutil's output says a certificate
// with this nickname is already in the database.
func alreadyInDatabase(out string) bool {
	return strings.Contains(strings.ToLower(out), "already exists")
}

func (n nss) remove(ctx context.Context, req Request) (Result, error) {
	result := Result{Store: n.name()}

	dirs, skip := n.check()
	if skip != "" {
		result.Skipped = true
		result.Reason = skip
		return result, nil
	}

	for _, profile := range dirs {
		// A profile that does not hold the authority is not a failure.
		_, _ = runCmd(ctx, CertutilBinary, nssRemoveArgs(req, profile)...)
	}

	result.Reason = plural(len(dirs))
	return result, nil
}

// check reports the profiles to edit, or the reason to skip the store.
//
//nolint:nonamedreturns // two returns of unrelated meaning at the same position; the names document which is which
func (nss) check() (dirs []string, skip string) {
	if _, err := exec.LookPath(CertutilBinary); err != nil {
		return nil, "certutil is absent, thus Firefox will not trust the authority"
	}
	dirs = profiles()
	if len(dirs) == 0 {
		return nil, "this machine has no Firefox profile"
	}
	return dirs, ""
}

// plural renders a count of profiles.
func plural(n int) string {
	if n == 1 {
		return "1 profile"
	}
	return strconv.Itoa(n) + " profiles"
}
