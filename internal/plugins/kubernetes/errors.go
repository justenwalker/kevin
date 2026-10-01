package kubernetes

// Error is a constant sentinel error.
type Error string

func (e Error) Error() string { return string(e) }

// ErrUnknownDriver reports that the with block names a driver that this
// plugin does not implement.
const ErrUnknownDriver = Error("kubernetes: unknown driver")

// ErrNoNodes reports that a cluster came up with no node, which means the
// driver failed in a way that it did not report.
const ErrNoNodes = Error("kubernetes: the cluster has no node")

// ErrNotTrusted reports that the system bundle of a node does not hold the
// kevin root certificate after the refresh.
const ErrNotTrusted = Error("kubernetes: the node does not trust the kevin root certificate")

// ErrNoClusterCIDRs reports that the kubeadm-config configmap carries no
// podSubnet or serviceSubnet - capture cannot safely exclude cluster-internal
// traffic without both.
const ErrNoClusterCIDRs = Error("kubernetes: the cluster reports no pod or service subnet")

// ErrNoRelayUDPPool reports that a udp expose entry has no UDP relay pool
// to draw from - the relay pod publishes none, typically because
// KEVIN_RELAY_UDP_POOL_SIZE is set to 0.
const ErrNoRelayUDPPool = Error("kubernetes: expose relay: no udp relay pool available")

// ErrContainerdNotReady reports that containerd did not answer within
// containerdReadyTimeout after a restart.
const ErrContainerdNotReady = Error("kubernetes: containerd did not become ready after the restart")

// ErrReservedNodeField reports that a control_plane or workers passthrough
// entry set a per-node kind config field kevin manages itself - "role", or
// the "kevin.node" label.
const ErrReservedNodeField = Error("kubernetes: kind: a node config passthrough may not set a field kevin manages")

// ErrInvalidNodeField reports that a control_plane or workers passthrough
// entry set a field kevin itself also populates - "labels" or
// "extraMounts" - to a value shaped unlike what kind itself expects
// there, so kevin cannot merge its own contribution into it.
const ErrInvalidNodeField = Error("kubernetes: kind: a node config passthrough field has the wrong shape")

// ErrK3dWorkerSettings reports that a workers entry carries node settings,
// which k3d has no create-time flag for.
const ErrK3dWorkerSettings = Error("kubernetes: k3d: a worker takes no node settings")

// ErrK3dReservedEnv reports that k3d.env sets a variable that kevin sets
// itself for the proxy.
const ErrK3dReservedEnv = Error("kubernetes: k3d: env may not set a proxy variable that kevin sets")

// ErrK3dReservedLabel reports that k3d.labels sets the kevin.node label, which
// kevin sets on every node.
const ErrK3dReservedLabel = Error("kubernetes: k3d: labels may not set the kevin.node label")

// ErrNotTCPAddr reports that a TCP listener did not report a TCP address.
const ErrNotTCPAddr = Error("kubernetes: the listener has no TCP address")

// ErrNetworkMismatch reports that a node container is not on the project
// network after it was connected.
const ErrNetworkMismatch = Error("kubernetes: the node is not on the project network")

// ErrNoRuntime reports that a driver call needs the container runtime, but
// the caller built the driver without one.
const ErrNoRuntime = Error("no container runtime")
