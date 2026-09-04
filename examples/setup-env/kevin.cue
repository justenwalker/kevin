// An environment that demonstrates the "setup" scope and a cross-scope
// "needs" reference from "env" into it.
//
// setup.db provisions something meant to persist across many `kevin run`s -
// a shared database, here just an echo step standing in for one. Bring it
// up once:
//
//	kevin -C examples/setup-env setup
//
// Then run the env scope as many times as you like; it depends on
// "setup.db" and reads the value setup.db's Export RPC reports back, via
// the "${setup.db.out.DSN}" expression:
//
//	kevin -C examples/setup-env run
//
// app never brings setup.db up itself - "kevin run" never starts the setup
// scope in-process - it only calls setup.db's Export RPC to resolve DSN.
// That's why setup.db must already be up (`kevin setup` first): Export
// reports back whatever the already-running step last configured, it does
// not start anything.
//
// Tear the persistent scope down when you're done with it:
//
//	kevin -C examples/setup-env teardown

project: "setup-env-example"

proxy: {
	listen:       "127.0.0.1:18120"
	gateway_port: 18121
}
console: listen: "127.0.0.1:18122"

plugins: echo: cmd: "../../bin/kevin-plugin-echo"

setup: {
	db: {
		uses:  "echo:echo"
		label: "Shared Database"
		with: {
			message: "provisioning shared database"
			export: DSN: "postgres://db.internal:5432/shared"
			export_sensitive: ["DSN"]
		}
	}
}

env: {
	app: {
		uses:  "echo:echo"
		label: "App"
		needs: ["setup.db"]
		with: message: "app connecting to ${setup.db.out.DSN}"
	}
}
