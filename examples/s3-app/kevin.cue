// A kind cluster and a MiniStack S3 emulator that persist across every
// "kevin run" (setup scope), plus a real application, redeployed on every
// run (env scope), that talks to the real s3.us-east-1.amazonaws.com
// endpoint with the unmodified aws-cli - no --endpoint-url, no local
// config. kevin's route interception is the only reason any of that
// traffic lands on the persistent, in-cluster MiniStack instead of the
// real internet, the same trick examples/intercept plays for a plain
// container, one layer deeper.
//
//	kevin -C examples/s3-app setup      # once: cluster + ministack + a seeded bucket
//	kevin -C examples/s3-app run        # every iteration: deploy/redeploy the app
//
// Watch it prove the interception:
//
//	KUBECONFIG=examples/s3-app/.kevin/kubeconfig/s3app kubectl logs -f deployment/app
//
// Each line shows a fresh heartbeat round-tripped through S3, plus the
// content "seed" wrote into a different bucket during "kevin setup" -
// still there, because the cluster and ministack never went away between
// runs. Ctrl-C run, then run it again: the app redeploys against the same
// cluster, nothing about setup gets recreated.
//
// s3_intercept has to live in env scope even though the cluster is
// setup-scope: a builtin:route registration only exists inside the process
// that made it, and "kevin setup" exits as soon as its own steps are up -
// only "kevin run" stays alive to actually proxy traffic. Its address
// still reaches the persistent cluster fine, cross-scope, through
// "${setup.cluster.out.relay_addr}".
//
// Tear the persistent scope down when you're done with it:
//
//	kevin -C examples/s3-app teardown

project: "s3-app-example"

proxy: {
	listen:       "127.0.0.1:18090"
	gateway_port: 18092
	egress: deny: true
}
console: listen: "127.0.0.1:18091"

setup: {
	cluster: {
		uses:  "builtin:kind"
		label: "Kind Cluster"
		with: {
			name: "s3app"
			// A pod pulls public images (ministack, aws-cli) through the
			// proxy. Allow Docker Hub, so the pull reaches the internet
			// instead of the deny page.
			egress: ["docker.io", "*.docker.io", "*.docker.com"]
			// Stands up the relay pod so relay_addr is exported for env's
			// route, even though nothing here uses "expose".
			relay: true
		}
	}
	ministack: {
		uses:  "builtin:kubectl"
		label: "MiniStack"
		needs: ["cluster"]
		with: {
			kubeconfig: "${needs.cluster.out.kubeconfig}"
			context:    "${needs.cluster.out.context}"
			path:       "manifests/ministack.yaml"
		}
	}
	ministack_ready: {
		uses:  "builtin:wait"
		label: "MiniStack Ready"
		needs: ["cluster", "ministack"]
		with: {
			timeout: "60s"
			kubectl: {
				kubeconfig: "${needs.cluster.out.kubeconfig}"
				context:    "${needs.cluster.out.context}"
				resource:   "deployment/ministack"
				rollout:    true
			}
		}
	}
	// seed pre-populates a bucket once, so every later "kevin run" can prove
	// it's reading data that outlived its own process.
	seed: {
		uses:  "builtin:kubectl"
		label: "Seed Bucket"
		needs: ["cluster", "ministack_ready"]
		with: {
			kubeconfig: "${needs.cluster.out.kubeconfig}"
			context:    "${needs.cluster.out.context}"
			path:       "manifests/seed-job.yaml"
		}
	}
	seed_ready: {
		uses:  "builtin:wait"
		label: "Seed Complete"
		needs: ["cluster", "seed"]
		with: {
			timeout: "90s"
			kubectl: {
				kubeconfig: "${needs.cluster.out.kubeconfig}"
				context:    "${needs.cluster.out.context}"
				resource:   "job/seed-bucket"
				for:        "condition=complete"
			}
		}
	}
}

env: {
	// Registers both the path-style and virtual-hosted-style S3 hostnames,
	// same pair examples/intercept uses for a plain container.
	s3_intercept: {
		uses:  "builtin:route"
		label: "Intercept S3"
		needs: ["setup.cluster"]
		with: {
			relay: "${setup.cluster.out.relay_addr}"
			routes: [
				{host: "s3.us-east-1.amazonaws.com", address: "ministack.default.svc.cluster.local:4566", external: true},
				{host: "*.s3.us-east-1.amazonaws.com", address: "ministack.default.svc.cluster.local:4566", external: true},
			]
		}
	}
	app: {
		uses:  "builtin:helm"
		label: "App"
		needs: ["setup.cluster", "s3_intercept"]
		with: {
			kubeconfig: "${setup.cluster.out.kubeconfig}"
			context:    "${setup.cluster.out.context}"
			release:    "app"
			chart:      "charts/app"
		}
	}
	app_ready: {
		uses:  "builtin:wait"
		label: "App Ready"
		needs: ["setup.cluster", "app"]
		with: {
			timeout: "60s"
			kubectl: {
				kubeconfig: "${setup.cluster.out.kubeconfig}"
				context:    "${setup.cluster.out.context}"
				resource:   "deployment/app"
				rollout:    true
			}
		}
	}
}
