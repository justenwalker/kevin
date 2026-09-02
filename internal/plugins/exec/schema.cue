#Config: {
	// up runs once. A nonzero exit fails this step, and its exit gates this
	// step's dependents. Its stdout, trimmed, is published as the "stdout"
	// output.
	up: #Exec

	// down runs on teardown. Omitted, Down does nothing.
	down?: #Exec

	// proxy adds the proxy variables and the CA to up and down, so that
	// their egress is visible.
	proxy?: bool | *false

	// egress lists the external hosts that up's command can reach. The
	// proxy denies egress by default. Only meaningful when proxy is true.
	egress?: [...string]
}

#Exec: {
	// command is the argv to run. There is no shell: use ["sh", "-c", "..."]
	// if a shell is needed.
	command!: [string, ...string]

	// cwd is the working directory, resolved against the project directory
	// if relative. Defaults to the project directory.
	cwd?: string

	// env sets additional environment variables for the command.
	env?: [string]: string
}
