#Config: {
	// driver is the tool that creates the cluster: "kind" or "k3d".
	driver!: "kind" | "k3d"

	// name is the cluster name. Defaults to "<project>-<step>".
	name?: string

	// workers creates one worker node for each key, in addition to the
	// control-plane node. The key names the node: kevin sets it as the
	// "kevin.node" node label, and builtin:fault accepts it in containers.
	// Each value takes the node settings described for "#NodeConfig".
	workers?: [string]: #NodeConfig

	// wait is how long to wait for the control plane to become ready, such
	// as "5m".
	wait?: string | *"5m"

	// retain keeps the nodes when the cluster fails to start, so you can
	// inspect them.
	retain?: bool

	// proxy sets the kevin proxy environment variables in every node.
	proxy?: bool | *true

	// egress lists external hosts that the nodes can reach when
	// proxy.egress.deny is true.
	egress?: [...string]

	// coredns configures the cluster DNS so that pods can resolve names on
	// the environment domain, such as "web.kevin.home".
	coredns?: bool | *true

	// trust_ca installs the kevin root certificate in every node, so that
	// image pulls through the kevin proxy succeed.
	trust_ca?: bool | *true

	// expose makes an address inside the cluster, such as a Service DNS name
	// and port, reachable from the host through a relay pod. The key names
	// the entry in the console. The step does not wait for the address to
	// accept connections: use a builtin:wait step with the "expose_<name>"
	// system value.
	expose?: [string]: #Expose

	// relay deploys the relay pod even when expose is empty, and sets the
	// relay_addr output. Use it with builtin:route to give a Service in the
	// cluster a name on the environment domain.
	relay?: bool | *false

	// mounts makes host paths visible in every node of the cluster.
	mounts?: [...#Mount]

	// kind holds the settings that only the "kind" driver has.
	kind?: #Kind

	// k3d holds the settings that only the "k3d" driver has.
	k3d?: #K3d

	if driver == "kind" {
		k3d?: _|_
	}
	if driver == "k3d" {
		kind?: _|_
	}
}

// #Mount is one host path that every node sees.
#Mount: {
	// host is the path on your machine. A relative path is relative to the
	// project directory.
	host!: string

	// container is the path inside each node.
	container!: string

	// readonly mounts the path read-only.
	readonly?: bool
}

// #Kind is the settings of the kind driver.
#Kind: {
	// image is the node image, such as "kindest/node:v1.34.0". Unset uses
	// the default of the installed kind version.
	image?: string

	// control_plane adds kind node settings to the control-plane node, with
	// kind's field names, such as extraMounts or kubeadmConfigPatches (see
	// https://kind.sigs.k8s.io/docs/user/configuration/#per-node-options).
	// labels and extraPortMappings add to the values kevin sets. The
	// "kevin.node" label and role cannot be set.
	control_plane?: #NodeConfig

	// config is a complete kind cluster configuration in YAML. It replaces
	// the configuration kevin generates: control_plane and workers have no
	// effect.
	config?: string
}

// #K3d holds the settings that only the k3d driver has.
#K3d: {
	// image is the k3s image, such as "rancher/k3s:v1.34.1-k3s1". Unset uses
	// the default of the installed k3d version.
	image?: string

	// disable turns off bundled k3s components.
	disable?: [...#K3sComponent]

	// env sets environment variables in every node. kevin sets the proxy
	// variables itself, so they are an error here.
	env?: [string]: string

	// memory limits the memory of each node, such as "2g".
	memory?: #MemoryLimit

	// labels adds labels to every node. The "kevin.node" label cannot be set.
	labels?: [string]: string
}

// #K3sComponent is a component that k3s bundles and k3d.disable can turn off.
#K3sComponent: "traefik" | "servicelb" | "metrics-server" | "local-storage"

// #MemoryLimit is a container memory limit: a number with an optional unit of
// b, k, m, or g.
#MemoryLimit: =~"^[0-9]+[bBkKmMgG]?$"

// #NodeConfig is the settings of one node. The "kind" driver accepts any
// kind node field. The "k3d" driver takes none.
#NodeConfig: {...}

#Expose: {
	// address is the in-cluster host:port to reach, such as
	// "postgres.default.svc.cluster.local:5432".
	address!: string

	// protocol is the transport protocol of the address.
	protocol?: "tcp" | "udp" | *"tcp"

	// host_port sets the port of the "forward_<name>" address on the host.
	// Unset, the OS picks a free port.
	host_port?: int
}
