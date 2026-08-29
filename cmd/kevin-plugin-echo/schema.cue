#Config: {
	// message is the text that the plugin writes to the log when the step
	// starts.
	message?: string

	// delay holds the step open for this duration, so that the parallel
	// execution is visible. The value is a Go duration, such as "500ms".
	delay?: string

	// fail makes the step fail. Use fail to test the removal of a partial
	// environment.
	fail?: bool

	// outputs are the values that the plugin publishes for dependent steps to
	// read.
	outputs?: [string]: string

	// export are the values Export reports for this step, for a
	// cross-scope "needs" reference or "kevin connect" to read.
	export?: [string]: string

	// export_sensitive lists which keys of export must be marked sensitive.
	export_sensitive?: [...string]

	// details are extra rows the step publishes for its console card.
	details?: [...{
		label:     string | *""
		value:     string
		copyable?: bool
		href?:     string
	}]
}