#Config: {
	// image is the container image to run.
	image!: string

	// pull fetches the image before the container starts.
	pull?: bool

	// cmd replaces the command of the image.
	cmd?: [...string]

	// entrypoint replaces the entrypoint of the image, such as ["sh", "-c"].
	// Unset keeps the image's own entrypoint.
	entrypoint?: [...string]

	// env holds extra environment variables for the container.
	env?: [string]: string

	// ports publish a container port on the host, such as "8080:80". A
	// workload reaches another workload through the docker network, thus a
	// published port is a convenience for a tool on the host.
	ports?: [...string]

	// volumes mount a host path, such as "/src:/dst:ro".
	volumes?: [...string]

	// proxy adds the proxy variables and the CA to the container, so that the
	// egress of the container is visible.
	proxy?: bool | *true

	// egress lists the external hosts that this container can reach. The proxy
	// denies egress by default.
	egress?: [...string]

	// start_timeout is the time to wait for the container to run. The value is
	// a Go duration.
	start_timeout?: string | *"30s"

	// expose publishes a container port on the host loopback. This is the
	// only way this step makes a port reachable outside the docker
	// network. Nothing routes by name here; the console and the ready log
	// report the address directly, and Outputs carries it too, as
	// host_80 for port 80 and so on. Pair it with a builtin:route step
	// to put a subdomain of the environment domain in front of it.
	expose?: [...#Expose]
}

#Expose: {
	// port is the container port to publish.
	port!: int

	// name labels this port in the console and in the ready log line.
	// Defaults to the port number.
	name?: string

	protocol?: "tcp" | "udp" | *"tcp"

	// host_port pins the port on the host. Omitted, the OS assigns one.
	host_port?: int
}
