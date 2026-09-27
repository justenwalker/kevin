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

	// ports publish a container port on the host, such as "8080:80". Steps
	// reach each other by step name and do not need a published port.
	ports?: [...string]

	// volumes mount a host path, such as "/src:/dst:ro".
	volumes?: [...string]

	// proxy mounts the kevin CA certificate in the container and sets
	// SSL_CERT_FILE to it, so the container trusts certificates from the
	// kevin proxy. Outbound traffic goes through the proxy either way.
	proxy?: bool | *true

	// egress lists external hosts that this container can reach when
	// proxy.egress.deny is true, in addition to proxy.egress.allow.
	egress?: [...string]

	// start_timeout is the maximum time to wait for the container to start,
	// as a duration such as "30s".
	start_timeout?: string | *"30s"

	// expose makes a container port reachable from the host, on 127.0.0.1.
	// The key names the entry in the console. The step is ready when each
	// published TCP port accepts connections. To give the port a name on
	// the environment domain, add a builtin:route step.
	expose?: [string]: #Expose
}

#Expose: {
	// port is the container port to publish.
	port!: int

	// protocol is the transport protocol of the port.
	protocol?: "tcp" | "udp" | *"tcp"

	// host_port sets the port on the host. Unset, the OS picks a free port.
	// Ignored when relay is true.
	host_port?: int

	// relay reaches the port through the relay container instead of a
	// published host port. Use it when host ports are limited. The address
	// is in the "expose_<name>" and "forward_<name>" system values instead of a
	// "host_<port>" output.
	relay?: bool | *false
}
