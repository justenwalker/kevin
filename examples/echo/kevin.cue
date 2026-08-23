// A kevin environment that creates no resource. The environment demonstrates
// the step DAG, and how kevin configures a provider.
//
//	kevin -C examples/echo run
//
// Step a runs first. Steps b and c run in parallel after a is up. Step d waits
// for the two of them. Step hold uses builtin:wait to pause for a while, so
// the environment stays up long enough to look at the console before it
// fails. Step boom uses the fail step type of the echo provider, and always
// fails. Step e needs boom, so e never runs.
//
// boom fails after hold is up, immediately canceling e - but run always
// blocks for an interrupt, failure or not, so the env stays up (Ctrl-C to
// end it) even with boom already failed. On interrupt, kevin removes a, b,
// c, d, and hold in reverse order and this example ends with a non-zero
// exit. This demonstrates that a failure stops its dependents and removes
// what came up.

project: "echo-example"

plugins: echo: {
	cmd: "../../bin/kevin-plugin-echo"
	config: greeting: "hello from the provider config"
}

env: {
	a: {
		uses:  "echo:echo"
		label: "Root"
		with: {
			message: "hello from a"
			outputs: greeting: "hi"
		}
	}
	b: {
		uses:  "echo:echo"
		label: "Parallel B"
		needs: ["a"]
		with: message: "b starts only after a is ready"
	}
	c: {
		uses:  "echo:echo"
		label: "Parallel C (delayed)"
		needs: ["a"]
		with: {
			message: "c runs at the same time as b"
			delay:   "300ms"
		}
	}
	d: {
		uses:  "echo:echo"
		label: "Join B+C"
		needs: ["b", "c"]
		with: message: "d waits for both b and c"
	}
	hold: {
		uses:  "builtin:wait"
		label: "Hold Open"
		needs: ["d"]
		with: duration: "10s"
	}
	boom: {
		uses:  "echo:fail"
		label: "Always Fails"
		needs: ["hold"]
	}
	e: {
		uses:  "echo:echo"
		label: "Never Runs"
		needs: ["boom"]
		with: message: "e never runs, because boom fails"
	}
}
