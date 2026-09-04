// A kevin environment with real containers.
//
//	kevin -C examples/web run
//
// web starts an nginx container. probe waits for web, then fetches the page
// through the docker network by the step name.
//
// The proxy serves web.kevin.home. Reach it from the host with:
//
//	curl --proxy http://127.0.0.1:18080 https://web.kevin.home/
//
// Without the CA trusted, curl needs the authority of the project directly:
//
//	curl --proxy http://127.0.0.1:18080 \
//	     --cacert .kevin/ca.crt https://web.kevin.home/
//
// Run `kevin ca install` once for the machine to drop the --cacert flag -
// see the quickstart's "Trust the CA" section.

project: "web-example"

proxy: {
	listen:       "127.0.0.1:18080"
	gateway_port: 18082
}
console: listen: "127.0.0.1:18081"

env: {
	web: {
		uses:  "builtin:container"
		label: "Web Server"
		with: {
			image:  "nginx:alpine"
			ports:  ["8080:80"]
			expose: web: {port: 80}
		}
	}
	// web_route puts web on the environment domain - the one mechanism for
	// that, whatever kind of step produced the address. A container step
	// never registers a route on its own; expose above only publishes the
	// port and reports where it landed.
	web_route: {
		uses:  "builtin:route"
		label: "Web Route"
		needs: ["web"]
		with: routes: [{host: "web", address: "${needs.web.out.host_80}"}]
	}
	probe: {
		uses:  "builtin:container"
		label: "Probe (through proxy)"
		needs: ["web"]
		with: {
			image: "busybox:stable"
			cmd: ["sh", "-c", "wget -qO- http://web.kevin.home/ | head -1 && sleep 3600"]
		}
	}
	// noproxy sets proxy: false, thus it carries no proxy environment at all.
	// It reaches web only through the DNS of the in-network relay.
	noproxy: {
		uses:  "builtin:container"
		label: "Probe (relay only)"
		needs: ["web"]
		with: {
			proxy: false
			image: "busybox:stable"
			cmd: ["sh", "-c", "wget -qO- http://web.kevin.home/ | head -1 && sleep 3600"]
		}
	}
}
