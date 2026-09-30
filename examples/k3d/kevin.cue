// A Kubernetes cluster with the k3d driver of builtin:kubernetes, a workload
// deployed into it with builtin:kubectl, and builtin:wait gating both the API
// server and the workload. It is the counterpart of examples/kind.
//
//	kevin -C examples/k3d run
//
// Use the cluster with the kubeconfig that the step publishes:
//
//	KUBECONFIG=.kevin/kubeconfig/k3d-example-cluster kubectl get nodes
//
// app_route registers "app" as a subdomain route into the cluster, through
// the SOCKS5 relay that the cluster step runs.
//
// Run `kevin ca install` once for the machine to trust the kevin root CA -
// see the quickstart's "Trust the CA" section.

project: "k3d-example"

proxy: {
	listen:       "127.0.0.1:18180"
	gateway_port: 18182
	// k3s pulls its own system images while the cluster starts, before the
	// step's egress list applies, so allow the registry here instead.
	egress: {
		deny: true
		allow: ["docker.io", "*.docker.io", "*.docker.com"]
	}
}
console: listen: "127.0.0.1:18181"

env: {
	cluster: {
		uses:  "builtin:kubernetes"
		label: "k3d Cluster"
		with: {
			driver: "k3d"
			wait:   "5m"
			workers: worker_a: {}
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
