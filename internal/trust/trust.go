// Package trust installs a certificate authority into the trust stores of the
// machine, and removes it again.
//
//	results, err := trust.Install(ctx, trust.Request{
//		CertPath:   "/path/to/ca.crt",
//		CommonName: "kevin demo CA",
//	})
//
// A store that the machine does not have is skipped, not an error. Read the
// returned results to report what happened.
package trust

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"regexp"
	"runtime"
	"strings"
)

// Request describes the authority to install.
type Request struct {
	// CertPath is the PEM certificate on disk. The field is required.
	CertPath string

	// CommonName is the subject of the authority. A removal matches on it.
	CommonName string

	// FileName is the base name to write into a directory store, without a
	// suffix. It defaults to a name built from CommonName.
	FileName string

	// System installs into the machine-wide store instead of the store of the
	// user. The machine-wide store needs root.
	System bool

	// Firefox installs into the certificate database of every Firefox
	// profile.
	Firefox bool
}

// Result reports what happened for one store.
type Result struct {
	// Store names the trust store, such as "macos-user" or "firefox".
	Store string

	// Installed is true when the authority reached the store.
	Installed bool

	// Skipped is true when the store is absent from this machine.
	Skipped bool

	// Reason explains a skip, or names the command to run by hand.
	Reason string
}

// Install puts the authority into every store that this machine has.
func Install(ctx context.Context, req Request) ([]Result, error) {
	return forEachTrustStore(ctx, req, func(s store) (Result, error) { return s.install(ctx, req) })
}

// Remove takes the authority out of every store that this machine has. A
// store that does not hold the authority is not an error.
func Remove(ctx context.Context, req Request) ([]Result, error) {
	return forEachTrustStore(ctx, req, func(s store) (Result, error) { return s.remove(ctx, req) })
}

func forEachTrustStore(_ context.Context, req Request, fn func(store) (Result, error)) ([]Result, error) {
	var results []Result
	for _, s := range stores(req) {
		result, err := fn(s)
		if err != nil {
			return append(results, result), err
		}
		results = append(results, result)
	}
	return results, nil
}

// store is one trust store.
type store interface {
	install(ctx context.Context, req Request) (Result, error)
	remove(ctx context.Context, req Request) (Result, error)
}

// stores lists the trust stores of this machine.
func stores(req Request) []store {
	var out []store
	switch runtime.GOOS {
	case "darwin":
		out = append(out, keychain{})
	case "linux":
		out = append(out, anchorDir{})
	}
	if req.Firefox {
		out = append(out, nss{})
	}
	return out
}

// fileNameUnsafe matches every run of characters that isn't a lowercase
// letter or digit.
var fileNameUnsafe = regexp.MustCompile(`[^a-z0-9]+`)

// FileNameFor builds a file name for a directory store.
func FileNameFor(req Request) string {
	if req.FileName != "" {
		return req.FileName
	}
	name := fileNameUnsafe.ReplaceAllString(strings.ToLower(req.CommonName), "-")
	return strings.Trim(name, "-")
}

// runCmd runs a command and returns its combined output.
func runCmd(ctx context.Context, name string, args ...string) (string, error) {
	//nolint:gosec // every argument comes from this package
	cmd := exec.CommandContext(ctx, name, args...)

	var buf bytes.Buffer
	cmd.Stdout = &buf
	cmd.Stderr = &buf

	if err := cmd.Run(); err != nil {
		return buf.String(), fmt.Errorf("trust: %s: %s: %w", name, strings.TrimSpace(buf.String()), err)
	}
	return buf.String(), nil
}

// isRoot reports whether the process can write a machine-wide store.
func isRoot() bool { return os.Geteuid() == 0 }

// quote renders a command for a user to copy and paste.
func quote(name string, args ...string) string {
	parts := make([]string, 0, len(args)+1)
	parts = append(parts, name)
	for _, a := range args {
		if strings.ContainsAny(a, " \t\"'") {
			a = `"` + strings.ReplaceAll(a, `"`, `\"`) + `"`
		}
		parts = append(parts, a)
	}
	return strings.Join(parts, " ")
}
