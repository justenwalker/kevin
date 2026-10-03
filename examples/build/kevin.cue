// A kevin environment that builds its image from a local Dockerfile.
//
//	kevin -C examples/build run
//
// app is built from ./app on every run, with a build argument, and runs as a
// non-root user with a memory limit. probe waits for app, then fetches the
// text file the Dockerfile wrote.

project: "build-example"

proxy: {
	listen:       "127.0.0.1:18080"
	gateway_port: 18082
	egress: deny: true
}
console: listen: "127.0.0.1:18081"

env: {
	app: {
		uses:  "builtin:container"
		label: "App (built)"
		with: {
			build: {
				context: "./app"
				args: GREETING: "hi"
			}
			user:   "1000:1000"
			memory: "64m"
			cpus:   "0.5"
		}
	}
	probe: {
		uses:  "builtin:container"
		label: "Probe"
		needs: ["app"]
		with: {
			image: "busybox:stable"
			cmd: ["sh", "-c", "wget -qO- http://app:8080/www.txt && sleep 3600"]
		}
	}
}
