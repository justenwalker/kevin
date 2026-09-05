//go:build !linux

package main

// applyCapture always fails on a non-Linux relay build: setns and nftables
// are Linux-only, and kevin-relay only ever ships as a linux/$ARCH image
// (see build/relay.Dockerfile and .goreleaser.yaml). This stub exists only
// so the package still builds on a contributor's own non-Linux machine.
func applyCapture(string, []int, selfAddrs) error {
	return ErrCaptureUnsupported
}
