// Package browser opens a URL in the user's default browser, and detects a
// browser kind from a User-Agent string.
package browser

import (
	"context"
	"fmt"
	"os/exec"
	"runtime"
)

// Open launches the system's default browser on url.
func Open(ctx context.Context, url string) error {
	name, args := command(runtime.GOOS, url)
	if err := exec.CommandContext(ctx, name, args...).Start(); err != nil { //nolint:gosec // name/args are a fixed OS opener, url is the console's own address
		return fmt.Errorf("browser: open %q: %w", url, err)
	}
	return nil
}

// command returns the OS opener command for url on goos (a runtime.GOOS
// value).
func command(goos, url string) (string, []string) {
	switch goos {
	case "darwin":
		return "open", []string{url}
	case "windows":
		return "rundll32", []string{"url.dll,FileProtocolHandler", url}
	default:
		return "xdg-open", []string{url}
	}
}
