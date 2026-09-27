#Config: {
	// relay is the address of a relay, such as
	// "${needs.cluster.out.relay_addr}" from a builtin:kind step. Set it when
	// the addresses are inside a kind cluster. Unset, the proxy connects to
	// each address directly.
	relay?: string

	// routes are the names that this step registers.
	routes: [...#Route]
}

#Route: {
	// host is the name to route. Without intercept, it is a subdomain of the
	// environment domain: "myapp" routes "myapp.<domain>". With intercept, it
	// is a full hostname, such as "s3.amazonaws.com". A leading "*." matches
	// any subdomain but not the name itself: "*.myapp" matches
	// "a.myapp.<domain>" but not "myapp.<domain>".
	host!: string

	// address is the target host:port. With relay, it is an address inside
	// the cluster, such as "myapp.default.svc.cluster.local:80". Without
	// relay, it is an address the host can connect to, such as a container
	// step's "host_<port>" output.
	address!: string

	// tls is true when address expects TLS.
	tls?: bool

	// intercept sends traffic for the real hostname in host to address, for
	// example to replace a cloud service with a local fake.
	intercept?: bool

	// ports lists the ports that clients use to connect to host. Has an
	// effect only when intercept is true.
	ports?: [...int] | *[443]

	// mode selects how the proxy handles connections to this route. "mitm"
	// terminates TLS with a certificate from the kevin CA and forwards each
	// request. "passthrough" forwards the client's TLS connection unchanged,
	// so the client checks the certificate of address; it requires tls:
	// true. "raw" forwards the TCP connection unchanged, for a protocol that
	// is not HTTP, such as a database protocol; it requires tls: false.
	mode: *"mitm" | "passthrough" | "raw"
	if mode == "passthrough" {
		tls: true
	}
	if mode == "raw" {
		tls: false
	}
}
