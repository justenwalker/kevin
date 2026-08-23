#Config: {
	// kubeconfig is a path, or a "${...}" expression that reads one from
	// needs, such as "${needs.cluster.out.kubeconfig}".
	kubeconfig!: string

	// context selects the kubeconfig context. Defaults to the kubeconfig's
	// current-context.
	context?: string

	// release is the Helm release name.
	release!: string

	// namespace is the target namespace.
	namespace?: string | *"default"

	// create_namespace creates namespace if it does not exist.
	create_namespace?: bool | *true

	// chart is a local path (relative to the project directory unless
	// absolute), an oci:// reference, or a chart name inside repo.
	chart!: string

	// repo is a chart repository URL. chart then names a chart inside it.
	repo?: string

	// version pins a chart version.
	version?: string

	// values_files are passed as repeated -f flags, in order. Relative to
	// the project directory unless absolute.
	values_files?: [...string]

	// post_renderer is a command that helm pipes rendered manifests through.
	post_renderer?: string

	// post_renderer_args are extra arguments for post_renderer.
	post_renderer_args?: [...string]

	// wait is how long to wait for the release to become ready, as a Go
	// duration. Empty disables --wait.
	wait?: string | *"5m"

	// atomic rolls the release back automatically on a failed upgrade.
	atomic?: bool | *true

	// keep leaves the release installed on Down, instead of uninstalling it.
	keep?: bool | *false
}
