#Config: {
	// name is the cluster name. It defaults to the step name, prefixed with
	// the project.
	name?: string

	// image is the node image, such as "kindest/node:v1.34.0". kind picks its
	// own default when this is empty.
	image?: string

	// control_plane passes additional per-node kind config through to the
	// control-plane node's generated entry - image, extraMounts,
	// extraPortMappings, kubeadmConfigPatches, and so on - using kind's own
	// field names directly (see kind's own per-node options:
	// https://kind.sigs.k8s.io/docs/user/configuration/#per-node-options).
	// Merged with what kevin itself generates for this node, not replacing
	// it: labels combine (a "kevin.node" key here is rejected - Up manages
	// that one itself), extraPortMappings combine (the relay's own mapping,
	// when one exists, stays alongside yours), and role may not be set at
	// all - it's structural, not configurable.
	control_plane?: #NodeConfig

	// workers names each worker node to create, on top of the one control
	// plane node - the map key is the node's own name, applied as a
	// Kubernetes node label ("kevin.node") so a dependent step (such as
	// builtin:fault) can address it by that name directly, instead of
	// kind's own "<cluster>-workerN" container naming. Each entry also
	// passes through additional per-node config the same way control_plane
	// does. A map, not a list, for the same reason expose is: one entry can
	// be added or changed without replacing the whole set.
	workers?: [string]: #NodeConfig

	// config is a kind cluster configuration in YAML. It replaces the
	// generated one, thus workers is ignored when this is set.
	config?: string

	// wait is how long to wait for the control plane to become ready. The
	// value is a Go duration.
	wait?: string | *"5m"

	// retain keeps the nodes when creation fails, so that the logs of a
	// broken cluster survive.
	retain?: bool

	// proxy passes the kevin proxy to the nodes. kind copies the proxy
	// variables into every node when it creates the cluster.
	proxy?: bool | *true

	// egress lists the external hosts that this cluster can reach.
	egress?: [...string]

	// coredns patches the cluster DNS to forward the environment domain to
	// the relay, so that a pod resolves a step. Set it to false to opt out.
	coredns?: bool | *true

	// trust_ca installs the kevin root certificate into every node, so a
	// pull through the proxy verifies. Set it to false to opt out.
	trust_ca?: bool | *true

	// expose lets a client outside the cluster dial an arbitrary in-cluster
	// address (a Service DNS name or a Pod IP, with its port) through a
	// single SOCKS5 relay pod inside the cluster, keyed by a name that
	// labels the entry in the console and the ready log line. Unlike a
	// container step, Up does not create what expose names. The target may
	// come from a manifest applied separately, after the cluster is up, so
	// Up does not wait for it to be dialable, only wires the relay and
	// reports the address. Up also reports each entry's relay address as
	// an "expose_<name>" output, for a downstream step (such as
	// builtin:wait) to read. A map, not a list, so one entry can be added
	// or changed without replacing the whole set.
	expose?: [string]: #Expose

	// relay deploys the SOCKS5 relay pod even with no expose entries, and
	// publishes its address as the "relay_addr" output. Set this to route
	// a subdomain into the cluster with builtin:route, without also
	// needing an expose entry.
	relay?: bool | *false
}

// #NodeConfig is an open passthrough for one node's kind config -
// control_plane's and each workers entry's value type. kevin doesn't chase
// every kind Node field it might want to expose, the same trade #Config's
// own config field already makes for the whole cluster.
#NodeConfig: {...}

#Expose: {
	// address is the in-cluster host:port to reach, such as
	// "postgres.default.svc.cluster.local:5432".
	address!: string

	// protocol is the wire protocol address speaks.
	protocol?: "tcp" | "udp" | *"tcp"

	// host_port pins the port of the local forward that lets a host
	// process dial this entry directly, reported as the "forward_<name>"
	// output. Omitted, the OS assigns one.
	host_port?: int
}
