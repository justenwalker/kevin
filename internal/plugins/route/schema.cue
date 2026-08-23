#Config: {
	// relay is the relay address to dial through, typically read from a
	// kind step's relay_addr output, e.g. "${needs.cluster.out.relay_addr}"
	// where cluster names that step. Unset means every route's address is
	// already something the proxy process can dial directly, such as a
	// container step's published loopback address.
	relay?: string

	// routes are the subdomains this step registers.
	routes: [...#Route]
}

#Route: {
	// host is the subdomain under the environment domain that serves this
	// route, e.g. "myapp". When external is true, host is instead a
	// real-world hostname used exactly as given, e.g. "s3.amazonaws.com" -
	// a leading "*." wildcard then matches any subdomain, e.g.
	// "*.s3.amazonaws.com", but not the bare domain itself.
	host!: string

	// address is the target: a Kubernetes Service DNS name and port when
	// relay is set ("myapp.default.svc.cluster.local:80"), or a
	// host-reachable address the proxy process can dial directly
	// otherwise ("127.0.0.1:8080").
	address!: string

	// tls is true when the target itself speaks TLS, such as a Service
	// fronting HTTPS on its port.
	tls?: bool

	// external is true when host is a real-world hostname to intercept,
	// rather than a subdomain of the environment domain - traffic meant
	// for that real service transparently lands on address instead, such
	// as a local fake running behind a container step.
	external?: bool
}
