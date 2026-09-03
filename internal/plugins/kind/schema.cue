#Config: {
	// name is the cluster name. It defaults to the step name, prefixed with
	// the project.
	name?: string

	// image is the node image, such as "kindest/node:v1.34.0". kind picks its
	// own default when this is empty.
	image?: string

	// workers is how many worker nodes to create, on top of the one control
	// plane node.
	workers?: int | *0

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
	// single SOCKS5 relay pod inside the cluster. Unlike a container step,
	// Up does not create what expose names. The target may come from a
	// manifest applied separately, after the cluster is up, so Up does not
	// wait for it to be dialable, only wires the relay and reports the
	// address. Up also reports each entry's relay address as an
	// "expose_<name>" output, for a downstream step (such as builtin:wait)
	// to read.
	expose?: [...#Expose]

	// relay deploys the SOCKS5 relay pod even with no expose entries, and
	// publishes its address as the "relay_addr" output. Set this to route
	// a subdomain into the cluster with builtin:route, without also
	// needing an expose entry.
	relay?: bool | *false

	// extra_mounts bind-mounts a host directory into the control-plane
	// node, such as a live source tree for a workload that expects one -
	// merged into the generated cluster config, so relay and expose still
	// work. Ignored when config is set: write mounts into your own raw
	// config instead.
	extra_mounts?: [...#ExtraMount]
}

#ExtraMount: {
	// host_path is the directory on the host to mount.
	host_path!: string

	// container_path is where it lands inside the node.
	container_path!: string
}

#Expose: {
	// address is the in-cluster host:port to reach, such as
	// "postgres.default.svc.cluster.local:5432".
	address!: string

	// name labels this entry in the console and the ready log line.
	// Defaults to address.
	name?: string
}
