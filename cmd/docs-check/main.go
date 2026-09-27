// Command docs-check reports broken internal links in a built Hugo site.
// Hugo fails a build on a relref to a missing page, but not on a missing
// #anchor; docs-check catches both.
//
//	docs-check <site-dir>
//
// site-dir must be built with baseURL "/". It exits 1 when any link is
// broken. `./build/gnob docs-check` builds the site and runs it.
package main

import (
	"fmt"
	"io"
	"os"
)

func main() {
	os.Exit(run(os.Args, os.Stdout, os.Stderr))
}

func run(args []string, stdout, stderr io.Writer) int {
	if len(args) != 2 {
		_, _ = fmt.Fprintln(stderr, "usage: docs-check <site-dir>")
		return 2
	}
	broken, err := checkLinks(args[1])
	if err != nil {
		_, _ = fmt.Fprintln(stderr, err)
		return 1
	}
	for _, b := range broken {
		_, _ = fmt.Fprintf(stdout, "%s: %s: missing %s\n", b.page, b.href, b.missing)
	}
	if len(broken) > 0 {
		_, _ = fmt.Fprintf(stderr, "docs-check: %d broken link(s)\n", len(broken))
		return 1
	}
	return 0
}
