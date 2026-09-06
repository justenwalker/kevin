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
	// route, e.g. "myapp" registers "myapp.<domain>". When intercept is
	// true, host is instead a real-world hostname used exactly as given,
	// e.g. "s3.amazonaws.com". Either way, a leading "*." wildcard matches
	// any subdomain but not the bare domain itself: "*.myapp" registers
	// "*.myapp.<domain>", matching "anything.myapp.<domain>" but not
	// "myapp.<domain>" - same rule the proxy's route table applies to
	// "*.s3.amazonaws.com" for an intercept entry.
	host!: string

	// address is the target: a Kubernetes Service DNS name and port when
	// relay is set ("myapp.default.svc.cluster.local:80"), or a
	// host-reachable address the proxy process can dial directly
	// otherwise ("127.0.0.1:8080").
	address!: string

	// tls is true when the target itself speaks TLS, such as a Service
	// fronting HTTPS on its port.
	tls?: bool

	// intercept is true when host is a real-world hostname to intercept,
	// rather than a subdomain of the environment domain - traffic meant
	// for that real service transparently lands on address instead, such
	// as a local fake running behind a container step.
	intercept?: bool

	// ports lists the ports a client actually dials host on, beyond 443,
	// which the relay always listens on - defaults to 443, the overwhelming
	// common case for a TLS API. Ignored unless intercept is true.
	ports?: [...int] | *[443]

	// mode selects how the proxy handles a client's connection to this
	// route - independent of tls, which only says whether address itself
	// speaks TLS:
	//   - "mitm" (default): terminate the client's TLS and re-sign it with
	//     kevin's own leaf, then route the decrypted request normally.
	//   - "passthrough": tunnel the client's TLS through untouched, so the
	//     client validates address's real certificate directly - useful for
	//     testing a workload's own TLS, such as a Service fronted by
	//     cert-manager inside a builtin:kind cluster. Requires tls: true;
	//     a plain-HTTP target has no certificate to pass through.
	//   - "raw": tunnel the connection byte for byte, with no TLS or HTTP
	//     assumption at all, for a raw TCP service such as a database's
	//     wire protocol. Requires tls: false.
	mode: *"mitm" | "passthrough" | "raw"
	if mode == "passthrough" {
		tls: true
	}
	if mode == "raw" {
		tls: false
	}
}
