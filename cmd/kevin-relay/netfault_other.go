//go:build !linux

package main

// applyFault and clearFault always fail on a non-Linux relay build: netem
// and setns are Linux-only, and kevin-relay only ever ships as a
// linux/$ARCH image (see build/relay.Dockerfile and .goreleaser.yaml).
// This stub exists only so the package still builds on a contributor's own
// non-Linux machine.
func applyFault(string, faultConfig) error { return ErrFaultUnsupported }

func clearFault(string, string) error { return ErrFaultUnsupported }
