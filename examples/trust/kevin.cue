// A kevin environment that installs the project authority into the trust
// stores of this machine.
//
//	kevin -C examples/trust setup      # install
//	kevin -C examples/trust teardown   # remove
//
// After setup, a browser and curl trust https://web.kevin.test without a
// --cacert flag.
//
// setup steps persist. `kevin run` does not touch them.

project: "trust-example"

setup: ca: {
	uses:  "builtin:trust"
	label: "Install CA"
	with: {
		// The store of the user needs no root. macOS still asks for a
		// confirmation of the change to the trust settings.
		system: false

		// Firefox keeps its own database. The step needs certutil from the
		// nss package, and reports a skip when certutil is absent.
		firefox: true
	}
}
