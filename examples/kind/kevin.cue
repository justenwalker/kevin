// A full kevin environment: a Kubernetes cluster, a workload deployed into
// it with builtin:kubectl and another with builtin:helm, and builtin:wait
// gating each of them plus a plain HTTP/TCP readiness check - exercising
// every builtin step type except container.
//
//	kevin -C examples/kind run
//
// The cluster nodes join the shared network as well as the network of kind,
// thus a container step and a pod reach each other.
//
// Use the cluster with the kubeconfig that the step publishes:
//
//	KUBECONFIG=.kevin/kubeconfig/kind-example-cluster kubectl get nodes
//
// Every builtin:wait check kind but exec shows up here:
//
//   - registry_ready: an http check against the registry container's own
//     API, reached directly (no relay needed - it's a published port).
//   - apiserver_ready: a tcp check that dials through a builtin:kind step's
//     SOCKS5 relay, reading the address from the cluster step's
//     "needs.cluster.system.expose_apiserver" value - the system
//     sub-namespace, kept separate from "out" so it can't collide with a
//     plugin-authored output.
//   - app_ready: a kubectl check using "rollout": true (kubectl rollout
//     status), gating on the Deployment app applies with builtin:kubectl.
//   - chart_ready: a kubectl check using "for" (kubectl wait --for=), gating
//     on the Deployment the hello chart installs with builtin:helm - with
//     the chart's own "wait" turned off, handing readiness off to wait
//     instead.
//
// app_route registers "app" as a subdomain route into the cluster,
// through the same SOCKS5 relay apiserver_ready dials - proxy traffic for
// that host reaches the app Service over a relayed connection, the same
// way a browser would reach any other step, no kubectl port-forward
// needed.
//
// `kevin -C examples/kind do nodes` runs kubectl against the cluster with
// no --kubeconfig flag to remember - the commands: block below renders it
// from the cluster step's own Export output.
//
// Run `kevin ca install` once for the machine to trust the kevin root CA -
// see the quickstart's "Trust the CA" section.

project: "kind-example"

proxy: listen: "127.0.0.1:18080"
console: listen: "127.0.0.1:18081"

env: {
	registry: {
		uses:  "builtin:container"
		label: "Local Registry"
		with: {
			image:  "registry:3"
			expose: registry: {port: 5000}
		}
	}
	registry_ready: {
		uses:  "builtin:wait"
		label: "Registry Ready"
		needs: ["registry"]
		with: {
			timeout: "10s"
			http: url: "http://${needs.registry.out.host_5000}/v2/"
		}
	}
	cluster: {
		uses:  "builtin:kind"
		label: "Kind Cluster"
		needs: ["registry"]
		with: {
			workers: 1
			wait:    "5m"
			// A pod pulls a public image through the proxy. Allow Docker Hub,
			// so the pull reaches the internet instead of the deny page.
			egress: ["docker.io", "*.docker.io", "*.docker.com"]
			expose: apiserver: address: "kubernetes.default.svc:443"
		}
	}
	apiserver_ready: {
		uses:  "builtin:wait"
		label: "API Server Ready"
		needs: ["cluster"]
		with: {
			timeout: "30s"
			tcp: address: "${needs.cluster.system.expose_apiserver}"
		}
	}
	app: {
		uses:  "builtin:kubectl"
		label: "App Deployment"
		needs: ["cluster"]
		with: {
			kubeconfig: "${needs.cluster.out.kubeconfig}"
			context:    "${needs.cluster.out.context}"
			manifest:   "apiVersion: apps/v1\nkind: Deployment\nmetadata:\n  name: app\nspec:\n  replicas: 1\n  selector:\n    matchLabels: {app: app}\n  template:\n    metadata:\n      labels: {app: app}\n    spec:\n      containers:\n      - name: app\n        image: nginx:alpine\n---\napiVersion: v1\nkind: Service\nmetadata:\n  name: app\nspec:\n  selector: {app: app}\n  ports:\n  - port: 80\n"
		}
	}
	app_ready: {
		uses:  "builtin:wait"
		label: "App Ready"
		needs: ["cluster", "app"]
		with: {
			timeout: "2m"
			kubectl: {
				kubeconfig: "${needs.cluster.out.kubeconfig}"
				context:    "${needs.cluster.out.context}"
				resource:   "deployment/app"
				rollout:    true
			}
		}
	}
	chart: {
		uses:  "builtin:helm"
		label: "Hello Chart"
		needs: ["cluster"]
		with: {
			kubeconfig: "${needs.cluster.out.kubeconfig}"
			context:    "${needs.cluster.out.context}"
			release:    "hello"
			chart:      "charts/hello"
			wait:       ""
		}
	}
	chart_ready: {
		uses:  "builtin:wait"
		label: "Chart Ready"
		needs: ["cluster", "chart"]
		with: {
			timeout: "2m"
			kubectl: {
				kubeconfig: "${needs.cluster.out.kubeconfig}"
				context:    "${needs.cluster.out.context}"
				resource:   "deployment/hello"
				for:        "condition=Available"
			}
		}
	}
	app_route: {
		uses:  "builtin:route"
		label: "App Route"
		needs: ["cluster", "app_ready"]
		with: {
			relay: "${needs.cluster.out.relay_addr}"
			routes: [{host: "app", address: "app.default.svc.cluster.local:80"}]
		}
	}
}

commands: nodes: {
	needs: ["cluster"]
	run: ["kubectl", "--kubeconfig", "${needs.cluster.out.kubeconfig}", "get", "nodes"]
}
