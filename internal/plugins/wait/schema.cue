#Config: {
	// timeout is the total time to wait for the check to succeed.
	timeout?: string | *"60s"

	// interval is how often to retry the check. It also bounds each
	// individual attempt: a dial, a request, a kubectl call, or a command
	// run.
	interval?: string | *"1s"

	// Exactly one of tcp, http, kubectl, exec, duration must be set.

	// tcp dials address until it accepts a connection.
	tcp?: #TCP

	// http requests url until it returns the expected status.
	http?: #HTTP

	// kubectl runs kubectl wait or kubectl rollout status against an
	// existing cluster.
	kubectl?: #Kubectl

	// exec retries a command until it exits zero.
	exec?: #Exec

	// duration sleeps for a fixed amount of time, such as "5s", instead of
	// checking anything. timeout and interval do not apply to it.
	duration?: string
}

#TCP: {
	// address is host:port to dial, or a "socks5://<relay>/<host:port>" URL,
	// the form a builtin:kind step's expose entries publish as a
	// "needs.<step>.system.expose_<name>" value, to dial through the
	// kind SOCKS5 relay instead of directly.
	address!: string
}

#HTTP: {
	// url is the address to request, such as
	// "http://${needs.api.out.host_8080}/healthz".
	url!: string

	// method is the HTTP method to use.
	method?: string | *"GET"

	// status is the response status code that counts as ready.
	status?: int | *200
}

#Kubectl: {
	// kubeconfig is a path, or a "${...}" expression that reads one from
	// needs, such as "${needs.cluster.out.kubeconfig}".
	kubeconfig!: string

	// context selects the kubeconfig context. Defaults to the kubeconfig's
	// current-context.
	context?: string

	// namespace is the target namespace.
	namespace?: string

	// resource is the target, such as "pod/mypod" or "deployment/api".
	resource!: string

	// Exactly one of for, rollout must be set.

	// for is the condition to wait for, passed to kubectl wait --for=, such
	// as "condition=Ready".
	for?: string

	// rollout runs kubectl rollout status against resource instead of
	// kubectl wait.
	rollout?: bool | *false
}

#Exec: {
	// command is the argv to run and retry. There is no shell: use
	// ["sh", "-c", "..."] if a shell is needed.
	command!: [string, ...string]
}
