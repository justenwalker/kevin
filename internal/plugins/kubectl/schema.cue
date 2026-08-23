#Config: {
	// kubeconfig is a path, or a "${...}" expression that reads one from
	// needs, such as "${needs.cluster.out.kubeconfig}".
	kubeconfig!: string

	// context selects the kubeconfig context. Defaults to the kubeconfig's
	// current-context.
	context?: string

	// namespace is the target namespace for a manifest that names none.
	namespace?: string

	// manifest is inline YAML to apply.
	manifest?: string

	// path is a manifest file or directory, applied with -f. Relative to the
	// project directory unless absolute.
	path?: string

	// kustomize is a kustomization directory, applied with kubectl -k.
	// Relative to the project directory unless absolute.
	kustomize?: string

	// Exactly one of manifest, path, kustomize must be set.

	// server_side applies with --server-side.
	server_side?: bool | *false

	// keep leaves the applied resources in place on Down, instead of
	// deleting them.
	keep?: bool | *false
}
