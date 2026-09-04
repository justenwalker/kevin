// The kitchen sink: every builtin step type in one environment, a real
// external API intercepted (AWS S3), and a real web app you browse through
// kevin's own proxy.
//
//	kevin -C examples/showcase setup    # once: cluster + ministack + seeded bucket
//	kevin -C examples/showcase run      # every iteration: intercept + app
//
// setup (persistent, survives every "kevin run"):
//
//   - cluster: a builtin:kind Kubernetes cluster.
//   - ministack/ministack_ready: builtin:kubectl deploys MiniStack (a free,
//     open-source AWS emulator, https://ministack.org/) into it, builtin:wait
//     gates on the rollout.
//   - seed_note: a builtin:exec step - runs on the host, not in a container -
//     whose stdout becomes ${needs.seed_note.out.stdout}.
//   - seed_note_config/seed/seed_ready: builtin:kubectl writes seed_note's
//     own output into a ConfigMap, then runs a Job (manifests/seed-job.yaml)
//     that waits for MiniStack, creates a bucket, and writes the
//     ConfigMap's value into it as an object body - proving needs/Export
//     data flows into an exec step same as any other.
//
// env (ephemeral, redeployed by every "kevin run"):
//
//   - s3_intercept: builtin:route, external: true, registers the real
//     s3.us-east-1.amazonaws.com hostnames (path-style and
//     virtual-hosted-style) into MiniStack - the same trick
//     examples/intercept and examples/s3-app use.
//   - session_upload: a plain builtin:container (not in the cluster) running
//     unmodified aws-cli through the host proxy - a second, independent code
//     path hitting the exact same interception as the in-cluster app.
//   - web: a step group wrapping the real workload - Nextcloud
//     (https://nextcloud.com), a real, widely deployed self-hosted file
//     server - deployed with builtin:helm, gated by builtin:wait, and
//     reachable at a subdomain with a second, plain builtin:route (no
//     external: true - a normal environment route, same trick
//     examples/kind's app_route uses).
//
// Browse to Nextcloud through kevin's proxy (trust the CA first - see the
// quickstart's "Trust the CA" section - or add --cacert .kevin/ca.crt):
//
//	https://nextcloud.<domain shown in the console>
//
// Log in with admin/admin - no setup wizard, a
// docker-entrypoint-hooks.d/before-starting script runs "occ
// maintenance:install" and "occ files_external:create" itself before
// apache ever starts. Its "S3" storage points at AWS's real endpoint,
// region us-east-1, key/secret test/test - the same throwaway credentials
// MiniStack accepts. Nothing in this chart or in kevin.cue tells the pod
// where MiniStack is - s3_intercept's own DNS interception (the relay
// answers the pod's own DNS query for the real hostname; no hostAliases
// entry, no proxy configuration of the pod's own) is the only reason any
// of that traffic lands on MiniStack instead of the real internet. Open
// the S3 folder and browse
// the bucket seed_note/seed wrote into during "kevin setup" - still there,
// because the cluster and MiniStack never went away between runs. The hook
// can still be running a few seconds after the step reports ready; reload
// if the S3 folder isn't
// listed yet.
//
// Poke the cluster directly - the commands: block below renders each one's
// kubeconfig/context from the setup-scope cluster step's own Export output,
// no --kubeconfig flag to remember:
//
//	kevin -C examples/showcase do kubectl -- get pods -A
//	kevin -C examples/showcase do k9s
//
// Tear the persistent scope down when you're done with it:
//
//	kevin -C examples/showcase teardown

project: "showcase-example"

proxy: {
	listen:       "127.0.0.1:18140"
	gateway_port: 18142
	egress: {
		deny: true
		// Pods pull public images (ministack, nextcloud) through the proxy.
		// This has to be environment-wide, not a step's own "egress" -
		// cluster's Up only runs during "kevin setup", but nextcloud's image
		// pulls during "kevin run", a separate later process with its own
		// proxy instance that never saw cluster's own egress list.
		allow: ["docker.io", "*.docker.io", "*.docker.com"]
	}
}
console: listen: "127.0.0.1:18141"

setup: {
	cluster: {
		uses:  "builtin:kind"
		label: "Kind Cluster"
		with: {
			name: "showcase"
			// Stands up the relay pod so relay_addr is exported for env's
			// routes, even though nothing here uses "expose".
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
	// seed_note runs on the host - the only builtin:exec step in this
	// environment - so seed's Job manifest below can prove a plain string
	// output flows into another step's config exactly like any plugin's.
	seed_note: {
		uses:  "builtin:exec"
		label: "Seed Note"
		with: up: command: ["sh", "-c", "echo seeded at $(date -u +%FT%TZ)"]
	}
	// seed_note_config carries seed_note's own output into a ConfigMap -
	// the only piece of the seed Job that's dynamic per run, so it's the
	// only piece still inline here instead of in manifests/seed-job.yaml.
	seed_note_config: {
		uses:  "builtin:kubectl"
		label: "Seed Note Config"
		needs: ["cluster", "seed_note"]
		with: {
			kubeconfig: "${needs.cluster.out.kubeconfig}"
			context:    "${needs.cluster.out.context}"
			manifest:   """
				apiVersion: v1
				kind: ConfigMap
				metadata:
				  name: seed-note
				data:
				  note: "${needs.seed_note.out.stdout}"
				"""
		}
	}
	// seed pre-populates a bucket once, with seed_note's own output (via
	// seed_note_config) as its content, so every later "kevin run" can
	// prove it's reading data that outlived its own process.
	seed: {
		uses:  "builtin:kubectl"
		label: "Seed Bucket"
		needs: ["cluster", "ministack_ready", "seed_note_config"]
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
	// same pair examples/intercept and examples/s3-app use. external: true
	// also reaches every pod's own DNS, not just the host proxy: kevin's
	// relay self-answers for either hostname (port 443, the default) so
	// nextcloud below needs no hostAliases entry or proxy env of its own.
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
	// A plain container, not in the cluster, running unmodified aws-cli
	// through the host proxy - a second, independent path hitting the exact
	// same interception the in-cluster app below relies on.
	session_upload: {
		uses:  "builtin:container"
		label: "Session Upload"
		needs: ["s3_intercept"]
		with: {
			image:      "amazon/aws-cli"
			entrypoint: ["sh", "-c"]
			env: {
				AWS_ACCESS_KEY_ID:     "test"
				AWS_SECRET_ACCESS_KEY: "test"
				AWS_DEFAULT_REGION:    "us-east-1"
				AWS_CA_BUNDLE:         "/usr/local/share/ca-certificates/kevin.crt"
			}
			cmd: ["""
				aws s3 mb s3://session || true
				date -u +%FT%TZ > /tmp/run.txt
				aws s3 cp /tmp/run.txt s3://session/run.txt
				aws s3 cp s3://session/run.txt -
				sleep 3600
				"""]
		}
	}
	// The real workload: Nextcloud, a real self-hosted file server, browsed
	// through kevin's own proxy. Wrapped in a step group so the console
	// collapses it to one row.
	web: {
		label: "Nextcloud"
		needs: ["setup.cluster", "s3_intercept"]
		steps: {
			app: {
				uses:  "builtin:helm"
				label: "Nextcloud"
				with: {
					kubeconfig: "${setup.cluster.out.kubeconfig}"
					context:    "${setup.cluster.out.context}"
					release:    "nextcloud"
					chart:      "charts/nextcloud"
				}
			}
			ready: {
				uses:  "builtin:wait"
				label: "Nextcloud Ready"
				needs: ["app"]
				with: {
					timeout: "60s"
					kubectl: {
						kubeconfig: "${setup.cluster.out.kubeconfig}"
						context:    "${setup.cluster.out.context}"
						resource:   "deployment/nextcloud"
						rollout:    true
					}
				}
			}
			app_route: {
				uses:  "builtin:route"
				label: "Nextcloud Route"
				needs: ["ready"]
				with: {
					relay:  "${setup.cluster.out.relay_addr}"
					routes: [{host: "nextcloud", address: "nextcloud.default.svc.cluster.local:80"}]
				}
			}
		}
		outputs: release: "${needs.app.out.release}"
	}
}

commands: {
	kubectl: {
		label: "kubectl"
		needs: ["setup.cluster"]
		run: ["kubectl", "--kubeconfig", "${setup.cluster.out.kubeconfig}", "--context", "${setup.cluster.out.context}"]
	}
	k9s: {
		label: "k9s"
		needs: ["setup.cluster"]
		run: ["k9s", "--kubeconfig", "${setup.cluster.out.kubeconfig}", "--context", "${setup.cluster.out.context}"]
	}
}
