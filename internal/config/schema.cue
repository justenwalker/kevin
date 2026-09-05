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

#StepGroup: {
	// steps declares the group's member steps. A member's own needs may name
	// a sibling member by that member's bare name - the group joins that
	// edge internally, a member never spells out the group's own name.
	steps!: [string]: #Step

	// needs lists the steps every member of the group implicitly depends on,
	// in addition to its own needs - a member does not redeclare these.
	needs?: [...string]

	// outputs computes the group's own outputs from its members', so a step
	// outside the group can needs the group as a single unit - a member is
	// not addressable from outside the group. Each value is a "${...}"
	// expression using the same needs.<member>.out.<key> convention a
	// member's own with block uses, scoped to this group's own members.
	outputs?: [string]: string

	// label is a friendly name for the console. Unset means the console
	// shows the group's own key instead.
	label?: string
}

// setup steps persist across runs. `kevin setup` starts them, and
// `kevin teardown` removes them.
setup: [string]: #Step | #StepGroup

// env steps are ephemeral. `kevin run` starts them, and removes them again on
// exit.
env: [string]: #Step | #StepGroup

#Command: {
	// needs lists the steps whose exported environment this command's run
	// merges in, the same "<step>" (env scope) / "setup.<step>" (setup
	// scope) convention #Step.needs uses.
	needs?: [...string]

	// run is the argv kevin execs in place of itself, inheriting the
	// caller's terminal - no shell; use ["sh", "-c", "..."] for shell
	// features.
	run!: [string, ...string]

	// cwd is the working directory. A relative path resolves against the
	// project directory, which is also the default.
	cwd?: string

	// label is a friendly name for the console, such as "Open a shell".
	label?: string
}

// commands run on demand with `kevin do <name>` - never as part of setup or
// env bring-up.
commands: [string]: #Command

proxy: {
	// listen is the proxy's primary, host-facing address. Must name a
	// real port - kevin does not pick one for you, so a builtin:kind
	// step's containerd config (baked in at cluster creation) and a
	// setup-scope proxy stay reachable across process restarts.
	listen: string & =~ "^.+:[1-9][0-9]*$"

	// gateway_port is the port the proxy's gateway listener binds - the
	// address the relay dials to reach the proxy from inside the docker
	// network. Must be a real port, for the same reason as listen.
	gateway_port: int & >0 & <=65535

	egress: {
		// allow lists the external hosts that every step can reach. The proxy
		// blocks a host that is absent from this list and absent from the
		// list of a step, when deny is true.
		allow: [...string] | *[]

		// deny blocks a host that no step allows and that allow omits. Must
		// be set explicitly - kevin does not pick a default, so a tag-driven
		// value (deny: someTag) unifies cleanly instead of silently losing
		// to a schema default (see the Egress control guide). Set it to
		// false for an environment that needs no such protection.
		deny: bool
	}
}

console: {
	// listen is the web console's host-facing address. Must name a real
	// port, for the same reason as proxy.listen: kevin does not pick one
	// for you.
	listen: string & =~ "^.+:[1-9][0-9]*$"
}

relay: {
	// image is the relay image. KEVIN_RELAY_IMAGE overrides it.
	image?: string
}
