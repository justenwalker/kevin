// #Index is the marker at the root of an index repo, or a plugin's
// version_source repo (kevin-index.yaml, sibling to plugins/), naming
// which directory layout version it uses. Only 1 is recognized.
#Index: {
	layout!: 1
}

// #PluginMeta is a plugin's identity within an index repo
// (plugins/<name>/plugin.yaml) - stable across every release.
#PluginMeta: {
	name!:       string & =~"^[a-z0-9]([a-z0-9-]*[a-z0-9])?$"
	summary!:    string
	homepage?:   string
	maintainer?: string

	// signers lists the plugin's acceptable release signers. It lives
	// here, not on a version, so a single compromised release can't
	// introduce a new trusted key on its own.
	signers?: [...#Signer]

	// version_source, when set, names a separate git repo holding this
	// plugin's own plugins/<name>/versions/ tree, instead of this
	// repo's own. Every version loaded from it must carry a detached
	// signature (minisign or sigstore), verified against signers - so
	// compromising this location alone can't forge a trusted release
	// without also compromising the index's own signer list.
	version_source?: string
}

// #Signer is one acceptable release signer for a plugin - a fresh,
// self-contained shape, not a reuse of #Minisign/#Sigstore (which
// describe a package's own signing requirement, not a plugin's set of
// trusted signers, and are close()d so they can't be extended).
#MinisignSigner: close({
	scheme!: "minisign"

	// key is the signer's raw minisign public key text (the
	// "untrusted comment: ...\n<base64>\n" minisign -Sm output).
	key!: string
})

#SigstoreSigner: close({
	scheme!:   "sigstore"
	identity!: string
	issuer!:   string
})

#Signer: #MinisignSigner | #SigstoreSigner

// #Version is one release of a plugin (plugins/<name>/versions/<semver>.yaml).
// source is the same #OCI/#File/#HTTP shape a kevin.cue plugins: entry
// uses - never #Cmd, meaningless for a remote catalog - so the two formats
// cannot hand-drift apart.
#Version: {
	// version repeats the file's own stem (versions/<version>.yaml), so a
	// copy/rename mistake is caught at validate time instead of silently
	// taking the filename's identity.
	version!: string & =~"^[0-9]+\\.[0-9]+\\.[0-9]+(-[0-9A-Za-z.-]+)?$"
	source!:  #OCI | #File | #HTTP
}
