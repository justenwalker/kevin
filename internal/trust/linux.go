package trust

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
)

// anchorDir is the Linux trust store. A distribution puts anchors in a
// directory, then rebuilds a bundle with a command.
type anchorDir struct{}

// anchorSuffix is the extension that every anchor directory expects.
const anchorSuffix = ".crt"

// anchorLayout is one distribution's anchor directory convention.
type anchorLayout struct {
	dir     string
	suffix  string
	rebuild string
}

// anchorLayouts are the layouts that anchorDir knows. The first directory
// that exists wins.
var anchorLayouts = []anchorLayout{
	// Debian and Ubuntu.
	{dir: "/usr/local/share/ca-certificates", suffix: anchorSuffix, rebuild: "update-ca-certificates"},
	// Fedora, RHEL, and SUSE.
	{dir: "/etc/pki/ca-trust/source/anchors", suffix: anchorSuffix, rebuild: "update-ca-trust"},
	// Alpine.
	{dir: "/usr/share/ca-certificates", suffix: anchorSuffix, rebuild: "update-ca-certificates"},
}

func (anchorDir) name() string { return "linux-system" }

// layout returns the anchor layout of this machine.
func (anchorDir) layout() (anchorLayout, bool) {
	for _, l := range anchorLayouts {
		if info, err := os.Stat(l.dir); err == nil && info.IsDir() {
			return l, true
		}
	}
	return anchorLayout{}, false
}

func (a anchorDir) install(ctx context.Context, req Request) (Result, error) {
	result := Result{Store: a.name()}

	l, ok := a.layout()
	if !ok {
		result.Skipped = true
		result.Reason = "this machine has no anchor directory"
		return result, nil
	}

	target := filepath.Join(l.dir, FileNameFor(req)+l.suffix)

	if !isRoot() {
		result.Reason = fmt.Sprintf("run: sudo cp %s %s && sudo %s", req.CertPath, target, l.rebuild)
		return result, fmt.Errorf("trust: %s: %w", l.dir, ErrNeedsRoot)
	}

	pem, err := os.ReadFile(req.CertPath)
	if err != nil {
		return result, fmt.Errorf("trust: read %s: %w", req.CertPath, err)
	}
	if err = os.WriteFile(target, pem, 0o644); err != nil { //nolint:gosec // a certificate is public
		return result, fmt.Errorf("trust: write %s: %w", target, err)
	}
	if _, err = runCmd(ctx, l.rebuild); err != nil {
		return result, err
	}

	result.Installed = true
	return result, nil
}

func (a anchorDir) remove(ctx context.Context, req Request) (Result, error) {
	result := Result{Store: a.name()}

	l, ok := a.layout()
	if !ok {
		result.Skipped = true
		result.Reason = "this machine has no anchor directory"
		return result, nil
	}

	target := filepath.Join(l.dir, FileNameFor(req)+l.suffix)

	if _, err := os.Stat(target); errors.Is(err, os.ErrNotExist) {
		result.Skipped = true
		result.Reason = "the anchor directory does not hold the authority"
		return result, nil
	}

	if !isRoot() {
		result.Reason = fmt.Sprintf("run: sudo rm %s && sudo %s", target, l.rebuild)
		return result, fmt.Errorf("trust: %s: %w", l.dir, ErrNeedsRoot)
	}

	if err := os.Remove(target); err != nil {
		return result, fmt.Errorf("trust: remove %s: %w", target, err)
	}
	if _, err := runCmd(ctx, l.rebuild); err != nil {
		return result, err
	}
	return result, nil
}
