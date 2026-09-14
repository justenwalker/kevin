//go:build linux

package main

import (
	"fmt"
	"os"

	"github.com/vishvananda/netlink"
	"github.com/vishvananda/netns"
)

// defaultFaultInterface is the interface applyFault/clearFault target when
// a request leaves Interface empty - kevin's own containers are
// single-interface on the project's docker network.
const defaultFaultInterface = "eth0"

// faultQdiscHandle is the handle applyFault/clearFault give the netem
// qdisc they install as an interface's root qdisc.
var faultQdiscHandle = netlink.MakeHandle(1, 0) //nolint:gochecknoglobals // a package-level constant computed once, not mutable state

// applyFault installs (or replaces) a netem qdisc on cfg.Interface inside
// the network namespace at netnsPath. Safe to call more than once for the
// same namespace/interface - QdiscReplace overwrites the qdisc's
// parameters wholesale, the same replace-not-accumulate contract
// applyCapture already has for nftables rules.
func applyFault(netnsPath string, cfg faultConfig) error {
	handle, err := faultNetnsHandle(netnsPath)
	if err != nil {
		return err
	}
	defer handle.Close()

	if err := buildFaultQdisc(handle, cfg); err != nil {
		return fmt.Errorf("relay: apply fault in %s: %w", netnsPath, err)
	}
	return nil
}

// buildFaultQdisc looks up cfg.Interface (or defaultFaultInterface, when
// empty) by name inside handle's namespace and replaces its root qdisc
// with a netem qdisc carrying cfg's impairments.
func buildFaultQdisc(handle *netlink.Handle, cfg faultConfig) error {
	link, err := handle.LinkByName(faultInterface(cfg.Interface))
	if err != nil {
		return fmt.Errorf("find interface %q: %w", faultInterface(cfg.Interface), err)
	}

	attrs := netlink.QdiscAttrs{LinkIndex: link.Attrs().Index, Handle: faultQdiscHandle, Parent: netlink.HANDLE_ROOT}
	netem := netlink.NewNetem(attrs, netlink.NetemQdiscAttrs{
		Latency:     uint32(cfg.DelayMS) * 1000, //nolint:gosec // delay_ms is a small, user-supplied duration, not attacker-controlled
		Jitter:      uint32(cfg.JitterMS) * 1000, //nolint:gosec // same as above
		Loss:        float32(cfg.LossPercent),
		Duplicate:   float32(cfg.DuplicatePercent),
		ReorderProb: float32(cfg.ReorderPercent),
		CorruptProb: float32(cfg.CorruptPercent),
	})
	if err := handle.QdiscReplace(netem); err != nil {
		return fmt.Errorf("replace netem qdisc: %w", err)
	}
	return nil
}

// clearFault removes the netem qdisc from cfg's interface inside the
// namespace at netnsPath, restoring its default qdisc. Not an error when
// no such qdisc exists, or the interface - or the namespace itself - is
// already gone: the caller (ClearFault, cmd/kevin-relay/fault.go) treats a
// gone target the same as an already-cleared one.
func clearFault(netnsPath, iface string) error {
	handle, err := faultNetnsHandle(netnsPath)
	if err != nil {
		return err
	}
	defer handle.Close()

	link, err := handle.LinkByName(faultInterface(iface))
	if err != nil {
		return fmt.Errorf("relay: find interface %q in %s: %w", faultInterface(iface), netnsPath, err)
	}
	qdisc := &netlink.Netem{QdiscAttrs: netlink.QdiscAttrs{LinkIndex: link.Attrs().Index, Handle: faultQdiscHandle, Parent: netlink.HANDLE_ROOT}}
	if err := handle.QdiscDel(qdisc); err != nil {
		return fmt.Errorf("relay: remove netem qdisc in %s: %w", netnsPath, err)
	}
	return nil
}

// faultInterface reports iface, or defaultFaultInterface when iface is
// empty.
func faultInterface(iface string) string {
	if iface == "" {
		return defaultFaultInterface
	}
	return iface
}

// faultNetnsHandle opens netnsPath and returns a netlink handle bound to
// that namespace - the same os.Open(netnsPath) fd applyCapture already
// uses for nftables, handed to vishvananda/netlink instead of
// google/nftables here.
func faultNetnsHandle(netnsPath string) (*netlink.Handle, error) {
	ns, err := os.Open(netnsPath) //nolint:gosec // netnsPath comes from the engine over the control channel, not user input
	if err != nil {
		return nil, fmt.Errorf("relay: open netns %s: %w", netnsPath, err)
	}
	defer func() { _ = ns.Close() }()

	handle, err := netlink.NewHandleAt(netns.NsHandle(ns.Fd()))
	if err != nil {
		return nil, fmt.Errorf("relay: connect netlink in %s: %w", netnsPath, err)
	}
	return handle, nil
}
