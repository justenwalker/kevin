// A kevin environment that creates no resource. The environment demonstrates
// step groups: a container with no behavior of its own that shares one
// implicit `needs` across its members and computes its own outputs from
// theirs.
//
//	kevin -C examples/groups run
//
// Step net comes up first. Group db has its own needs: ["net"], so both of
// its members, primary and replica, get that dependency without redeclaring
// it. replica additionally needs primary, named by its own bare name - the
// group joins that edge internally, not as db.primary. db's outputs block
// computes addr from primary's own output; web, outside the group entirely,
// reads it exactly like it would read a plain step's output. hold keeps the
// environment up so the console sidebar can be inspected: db collapses to a
// single row, expanding to reveal primary and replica. Ctrl-C to end it.

project: "groups-example"

proxy: {
	listen:       "127.0.0.1:18130"
	gateway_port: 18131
	egress: deny: true
}
console: listen: "127.0.0.1:18132"

plugins: echo: cmd: "../../bin/kevin-plugin-echo"

env: {
	net: {
		uses:  "echo:echo"
		label: "Network"
		with: {
			message: "network ready"
			outputs: addr: "10.0.0.1"
		}
	}
	db: {
		label: "Database"
		needs: ["net"]
		steps: {
			primary: {
				uses:  "echo:echo"
				label: "Primary"
				with: {
					message: "primary got ${needs.net.out.addr}"
					outputs: addr: "10.0.0.2"
				}
			}
			replica: {
				uses:  "echo:echo"
				label: "Replica"
				needs: ["primary"]
				with: message: "replica got ${needs.primary.out.addr}"
			}
		}
		outputs: addr: "${needs.primary.out.addr}"
	}
	web: {
		uses:  "echo:echo"
		label: "Web"
		needs: ["db"]
		with: message: "web got ${needs.db.out.addr}"
	}
	hold: {
		uses:  "builtin:wait"
		label: "Hold Open"
		needs: ["web"]
		with: duration: "1h"
	}
}
