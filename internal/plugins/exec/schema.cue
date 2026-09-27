#Config: {
	// up runs once when the step starts. A nonzero exit fails the step. Its
	// stdout, with surrounding whitespace removed, is the "stdout" output.
	up: #Exec

	// down runs on teardown. Unset, teardown runs nothing.
	down?: #Exec

	// proxy sets the proxy environment variables and the kevin CA for up and
	// down, so their requests go through the kevin proxy and show in the
	// console.
	proxy?: bool | *false

	// egress lists external hosts that the commands can reach when
	// proxy.egress.deny is true. Has an effect only when proxy is true.
	egress?: [...string]
}

#Exec: {
	// command is the program and its arguments. There is no shell: use
	// ["sh", "-c", "..."] for shell features.
	command!: [string, ...string]

	// cwd is the working directory. A relative path resolves against the
	// project directory, which is also the default.
	cwd?: string

	// env sets additional environment variables for the command.
	env?: [string]: string
}
