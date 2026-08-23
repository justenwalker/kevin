// Core schema of a kevin environment. kevin unifies this schema with the
// kevin.cue file of the user before anything runs.

// project names the environment. project is also the prefix of every resource
// that kevin creates. The default is the name of the project directory.
project: string | *""

// domain is the base name of the environment. A step is reachable through the
// proxy at <step>.<domain>, and the proxy serves a proxy.pac that sends this
// domain through it.
domain: string | *"kevin.home"

// plugins declares the providers of this environment. The key is a plugin
// name: lowercase letters, digits, and hyphens. The key holds no colon.
plugins: close({[=~"^[a-z0-9]([a-z0-9-]*[a-z0-9])?$"]: #Plugin})

// #PluginSource holds what every plugin source kind accepts.
#PluginSource: {
	// config configures the plugin itself. kevin validates config against
	// the config schema that the plugin publishes.
	config?: {...}
}

// #Package is a plugin source that ships as an archive with its own
// manifest - oci:, file:, and http: all fetch one; cmd: does not.
#Package: {
	#PluginSource

	// args override the package manifest's own args, in full.
	args?: [...string]

	// env adds environment variables to the plugin process. A package
	// manifest never sets these - env always comes from kevin.cue.
	env?: [string]: string

	// signed requires a valid minisign signature from a key in the local
	// trust store (`kevin plugin trust`) before the package is extracted.
	// Trust lives outside kevin.cue on purpose: editing this file alone
	// can never add a trusted signer.
	signed: bool | *false
}

// #Downloadable is a #Package fetched over a plain URL or from disk -
// unlike oci:, neither carries a built-in digest, so both offer a pin.
#Downloadable: {
	#Package

	// checksum pins the sha256 of the package tar ("sha256:<hex>"), checked
	// before anything inside it is parsed.
	checksum?: string
}

// #Plugin is a choice of sources. A source says how kevin obtains the
// plugin binary.
#Plugin: #Cmd | #OCI | #File | #HTTP

#Cmd: {
	#PluginSource

	// cmd is the plugin binary. The project directory resolves a relative
	// path.
	cmd!: string

	// args are the arguments for the binary.
	args?: [...string]

	// env adds environment variables to the plugin process.
	env?: [string]: string
}

#OCI: {
	#Package

	// oci is the reference of the plugin package: an OCI artifact whose
	// single layer is the same tar (optionally gzip-compressed) format
	// #File extracts. A tag ("host/repo:tag") or a pinned digest
	// ("host/repo@sha256:...")
	oci!: string
}

#File: {
	#Downloadable

	// file is the path to the package tar, resolved against the project
	// directory.
	file!: string
}

#HTTP: {
	#Downloadable

	// http is the URL of the plugin package: a tar (optionally
	// gzip-compressed) archive in the same format #File extracts.
	http!: string
}

#Step: {
	// uses names the step type that implements this step. The value has the
	// form <plugin>:<step>.
	uses!: string

	// needs lists the steps that must start first. Steps that do not depend on
	// each other run in parallel.
	needs?: [...string]

	// with is the step configuration. kevin validates with against the schema
	// of the plugin before the environment runs.
	with?: {...}

	// label is a friendly name for the console, such as "Web Server". The
	// step's own key still names it everywhere else - needs, the domain, the
	// event log. Unset means the console shows the key instead.
	label?: string
}

// setup steps persist across runs. `kevin setup` starts them, and
// `kevin teardown` removes them.
setup: [string]: #Step

// env steps are ephemeral. `kevin run` starts them, and removes them again on
// exit.
env: [string]: #Step

proxy: {
	// listen is the proxy address. Port 0 selects a free port.
	listen: string | *"127.0.0.1:0"

	egress: {
		// allow lists the external hosts that every step can reach. The proxy
		// denies egress by default. The proxy blocks a host that is absent
		// from this list and absent from the list of a step.
		allow: [...string] | *[]

		// deny blocks a host that no step allows and that allow omits. Set it
		// to false for an environment that needs no such protection.
		deny: bool | *true
	}
}

console: {
	// listen is the web console address. Port 0 selects a free port.
	listen: string | *"127.0.0.1:0"
}

relay: {
	// image is the relay image. KEVIN_RELAY_IMAGE overrides it.
	image?: string

	// enabled starts the relay. Set it to false for an environment that
	// needs no in-network name resolution.
	enabled: bool | *true
}
