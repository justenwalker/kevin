package kind

// Error is a constant sentinel error.
type Error string

func (e Error) Error() string { return string(e) }

// ErrNoNodes reports that a cluster came up with no node, which means kind
// failed in a way that it did not report.
const ErrNoNodes = Error("kind: the cluster has no node")

// ErrContainerdNotReady reports that containerd did not answer within
// containerdReadyTimeout after a restart.
const ErrContainerdNotReady = Error("kind: containerd did not become ready after the restart")

// ErrNotTrusted reports that the system bundle of a node does not hold the
// kevin root certificate after the refresh.
const ErrNotTrusted = Error("kind: the node does not trust the kevin root certificate")

// ErrNoControlPlaneNode reports that no node's name matched the
// control-plane naming convention.
const ErrNoControlPlaneNode = Error("kind: no control-plane node found")

// ErrNoClusterCIDRs reports that the kubeadm-config configmap carries no
// podSubnet or serviceSubnet - capture cannot safely exclude cluster-internal
// traffic without both.
const ErrNoClusterCIDRs = Error("kind: the cluster reports no pod or service subnet")

// ErrReservedNodeField reports that a control_plane or workers passthrough
// entry set a per-node kind config field kevin manages itself - "role", or
// the "kevin.node" label.
const ErrReservedNodeField = Error("kind: a node config passthrough may not set a field kevin manages")

// ErrInvalidNodeField reports that a control_plane or workers passthrough
// entry set a field kevin itself also populates - "labels" or
// "extraPortMappings" - to a value shaped unlike what kind itself expects
// there, so kevin cannot merge its own contribution into it.
const ErrInvalidNodeField = Error("kind: a node config passthrough field has the wrong shape")
