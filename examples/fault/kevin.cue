// A kevin environment demonstrating builtin:fault: netem-style network
// chaos injected directly into a container's network namespace, with no
// interaction with builtin:route or the kevin proxy at all - fault targets
// the container's namespace directly, so this demo reaches it on its
// published host port, not through the environment domain.
//
//	kevin -C examples/fault run
//
// backend runs a plain HTTP echo server, published on 127.0.0.1:8080.
// backend_fault then delays every packet on its namespace by 500ms and
// drops 25% of them. Compare a curl against backend's published port
// before and after backend_fault comes up:
//
//	curl -w '%{time_total}\n' -o /dev/null -s http://127.0.0.1:8080/
//
// Before backend_fault: near-instant, always succeeds. After: >=0.5s of
// added latency, and roughly a quarter of repeated requests time out or
// fail outright from the induced loss - loop the curl above a few times
// to see it.

project: "fault-example"

proxy: {
	listen:       "127.0.0.1:18150"
	gateway_port: 18151
	egress: deny: true
}
console: listen: "127.0.0.1:18152"

env: {
	backend: {
		uses:  "builtin:container"
		label: "Backend"
		with: {
			image: "hashicorp/http-echo"
			cmd: ["-text=hello from kevin", "-listen=:5678"]
			ports: ["8080:5678"]
		}
	}
	// backend_fault targets backend's own network namespace directly - no
	// route, no proxy, just the container's netns.
	backend_fault: {
		uses:  "builtin:fault"
		label: "Network Fault"
		needs: ["backend"]
		with: {
			delay_ms:     500
			loss_percent: 25.0
		}
	}
}
