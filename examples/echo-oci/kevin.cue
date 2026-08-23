// A kevin environment that fetches its plugin from an OCI registry instead
// of running a local binary, demonstrating the `oci` plugin source and
// minisign-signed provenance instead of the `file`/`cmd` sources the other
// examples use.
//
//	kevin plugin trust add ~/.minisign/minisign.pub   # once, if not done already
//	kevin -C examples/echo-oci run
//
// kevin fetches ghcr.io/justenwalker/kevin/plugin-echo:0.0.1, verifies its
// minisign signature against the local trust store, caches the blob under
// ~/.kevin/pkg-cache/, and extracts it into .kevin/plugins/echo/ - same
// provider as examples/echo, just packaged and signed.

project: "echo-oci-example"

plugins: echo: {
	oci:    "ghcr.io/justenwalker/kevin/plugin-echo:0.0.1"
	signed: true
	config: greeting: "hello from the OCI-packaged provider"
}

env: {
	a: {
		uses:  "echo:echo"
		label: "Root"
		with: message: "hello from an OCI-fetched plugin"
	}
}
