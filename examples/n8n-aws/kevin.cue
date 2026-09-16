// n8n (open-source workflow automation, https://n8n.io/), deployed the way
// it actually runs in production - a queue-mode stack: a main process, a
// separate worker process, Postgres, and Redis - running a real workflow
// that touches three real AWS APIs (S3, SQS, SES) through kevin's own
// interception, landing on MiniStack instead of the real internet. n8n's
// AWS nodes are unmodified: no custom endpoint, no local config - the same
// "real client, real hostname" story examples/intercept and examples/s3-app
// tell for S3 alone, extended across a whole multi-service stack.
//
//	kevin -C examples/n8n-aws run
//
// ministack/ministack_ready/aws_intercept stand up the AWS emulator and
// register the real s3.us-east-1.amazonaws.com (plus its wildcard
// subdomain, for aws-cli's and n8n's own virtual-hosted-style bucket
// access), sqs.us-east-1.amazonaws.com and email.us-east-1.amazonaws.com
// hostnames as intercepts into it. seed_aws runs unmodified aws-cli through
// that interception to create the bucket and queue the workflow uses and
// verify the sender address SES will send from - MiniStack's own identity
// verification "jumps straight to Success", so this is instant.
//
// postgres/redis back n8n's own queue-mode deployment: n8n_main serves the
// UI, API, and webhooks; n8n_worker is the separate process that actually
// executes a workflow's nodes - the point of queue mode. seed_n8n drives
// n8n's own REST API (data/seed_n8n.py) to create the instance's owner
// account, an AWS credential with the same throwaway test/test
// credentials aws-cli uses, and the demo workflow itself
// (data/workflow.json: a webhook trigger, an S3 upload, an SQS
// SendMessage, and an SES email), then publishes it so its webhook goes
// live. trigger_workflow then polls that webhook (a builtin:wait, not a
// plain readiness check) until it answers 200, so the DAG itself proves the
// whole path end to end: n8n_main receives the webhook, hands the job to
// n8n_worker over Redis, and the worker's own AWS nodes land on MiniStack.
//
// ministack_fault adds mild network chaos to MiniStack - real AWS isn't
// instant or perfectly reliable either - the only example combining
// builtin:fault with builtin:route interception.
//
// Watch it happen:
//
//	kevin -C examples/n8n-aws do trigger   # re-run the workflow by hand
//	kevin -C examples/n8n-aws do psql -- -c "select id, name, active from workflow_entity;"
//
// Or browse the n8n UI itself through kevin's proxy (trust the CA first -
// see the quickstart's "Trust the CA" section, or add --cacert
// .kevin/ca.crt) at https://n8n.<domain shown in the console>, sign in as
// admin@example.com / KevinDemo123!, and open the "AWS Roundtrip"
// workflow's execution list.

project: "n8n-aws-example"

proxy: {
	listen:       "127.0.0.1:18160"
	gateway_port: 18162
	egress: deny: true
}
console: listen: "127.0.0.1:18161"

env: {
	ministack: {
		uses:  "builtin:container"
		label: "MiniStack"
		with: {
			image:  "ministackorg/ministack"
			expose: main: {port: 4566}
		}
	}
	ministack_ready: {
		uses:  "builtin:wait"
		label: "MiniStack Ready"
		needs: ["ministack"]
		with: {
			timeout: "30s"
			http: url: "http://${needs.ministack.out.host_4566}/"
		}
	}
	// Registers the real AWS hostnames this environment intercepts. Every
	// address here is a plain container's loopback address, so the proxy
	// dials it directly - same as examples/intercept.
	aws_intercept: {
		uses:  "builtin:route"
		label: "Intercept AWS"
		needs: ["ministack", "ministack_ready"]
		with: routes: [
			{host: "s3.us-east-1.amazonaws.com", address: "${needs.ministack.out.host_4566}", intercept: true},
			{host: "*.s3.us-east-1.amazonaws.com", address: "${needs.ministack.out.host_4566}", intercept: true},
			{host: "sqs.us-east-1.amazonaws.com", address: "${needs.ministack.out.host_4566}", intercept: true},
			{host: "email.us-east-1.amazonaws.com", address: "${needs.ministack.out.host_4566}", intercept: true},
		]
	}
	// seed_aws runs the real, unmodified aws-cli through the interception
	// above to create the bucket and queue the workflow uses, and verify
	// the address it sends mail from - a second, independent code path
	// hitting the exact same interception the n8n workflow relies on, the
	// same role examples/showcase's session_upload plays.
	seed_aws: {
		uses:  "builtin:container"
		label: "Seed AWS"
		needs: ["aws_intercept"]
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
				set -e
				aws s3 mb s3://kevin-demo
				aws sqs create-queue --queue-name kevin-demo
				aws ses verify-email-identity --email-address kevin@example.com
				sleep 3600
				"""]
		}
	}
	postgres: {
		uses:  "builtin:container"
		label: "Postgres"
		with: {
			image: "postgres:16-alpine"
			env: {
				POSTGRES_USER:     "n8n"
				POSTGRES_PASSWORD: "n8n"
				POSTGRES_DB:       "n8n"
			}
			expose: db: {port: 5432}
		}
	}
	postgres_ready: {
		uses:  "builtin:wait"
		label: "Postgres Ready"
		needs: ["postgres"]
		with: {
			timeout: "30s"
			tcp: address: "${needs.postgres.out.host_5432}"
		}
	}
	redis: {
		uses:  "builtin:container"
		label: "Redis"
		with: {
			image:  "redis:7-alpine"
			expose: cache: {port: 6379}
		}
	}
	redis_ready: {
		uses:  "builtin:wait"
		label: "Redis Ready"
		needs: ["redis"]
		with: {
			timeout: "30s"
			tcp: address: "${needs.redis.out.host_6379}"
		}
	}
	// n8n_main serves the UI, the REST API, and every webhook. Queue mode
	// hands the actual node execution off to n8n_worker below over Redis -
	// the two containers only agree on the same job because they share one
	// N8N_ENCRYPTION_KEY: n8n generates one itself when unset, but each
	// container would generate its own, and a worker can't decrypt a
	// credential main encrypted with a key it never saw.
	n8n_main: {
		uses:  "builtin:container"
		label: "n8n Main"
		needs: ["postgres_ready", "redis_ready", "aws_intercept"]
		with: {
			image: "n8nio/n8n:2.39.5"
			cmd: ["start"]
			env: {
				EXECUTIONS_MODE:        "queue"
				QUEUE_BULL_REDIS_HOST:  "redis"
				QUEUE_BULL_REDIS_PORT:  "6379"
				DB_TYPE:                "postgresdb"
				DB_POSTGRESDB_HOST:     "postgres"
				DB_POSTGRESDB_PORT:     "5432"
				DB_POSTGRESDB_DATABASE: "n8n"
				DB_POSTGRESDB_USER:     "n8n"
				DB_POSTGRESDB_PASSWORD: "n8n"
				N8N_ENCRYPTION_KEY:     "kevin-demo-encryption-key-not-secret"
				// Node needs its CA path set explicitly to trust the
				// proxy's MITM'd connections.
				NODE_EXTRA_CA_CERTS:               "/usr/local/share/ca-certificates/kevin.crt"
				N8N_DIAGNOSTICS_ENABLED:           "false"
				N8N_VERSION_NOTIFICATIONS_ENABLED: "false"
				N8N_TEMPLATES_ENABLED:             "false"
				N8N_HIRING_BANNER_ENABLED:         "false"
			}
			expose: web: {port: 5678}
		}
	}
	// Waits on the REST router itself, since seed_n8n needs it mounted.
	n8n_main_ready: {
		uses:  "builtin:wait"
		label: "n8n Main Ready"
		needs: ["n8n_main"]
		with: {
			timeout: "60s"
			http: url: "http://${needs.n8n_main.out.host_5678}/rest/settings"
		}
	}
	n8n_worker: {
		uses:  "builtin:container"
		label: "n8n Worker"
		needs: ["postgres_ready", "redis_ready", "aws_intercept"]
		with: {
			image: "n8nio/n8n:2.39.5"
			cmd: ["worker"]
			env: {
				EXECUTIONS_MODE:                   "queue"
				QUEUE_BULL_REDIS_HOST:             "redis"
				QUEUE_BULL_REDIS_PORT:             "6379"
				DB_TYPE:                           "postgresdb"
				DB_POSTGRESDB_HOST:                "postgres"
				DB_POSTGRESDB_PORT:                "5432"
				DB_POSTGRESDB_DATABASE:            "n8n"
				DB_POSTGRESDB_USER:                "n8n"
				DB_POSTGRESDB_PASSWORD:            "n8n"
				N8N_ENCRYPTION_KEY:                "kevin-demo-encryption-key-not-secret"
				NODE_EXTRA_CA_CERTS:               "/usr/local/share/ca-certificates/kevin.crt"
				N8N_DIAGNOSTICS_ENABLED:           "false"
				N8N_VERSION_NOTIFICATIONS_ENABLED: "false"
				N8N_TEMPLATES_ENABLED:             "false"
				N8N_HIRING_BANNER_ENABLED:         "false"
			}
		}
	}
	// A worker exposes no health port - a fixed pause gives it time to
	// connect to Redis before trigger_workflow needs it.
	n8n_worker_ready: {
		uses:  "builtin:wait"
		label: "n8n Worker Ready"
		needs: ["n8n_worker"]
		with: duration: "5s"
	}
	ministack_fault: {
		uses:  "builtin:fault"
		label: "Flaky AWS"
		needs: ["ministack"]
		with: {
			delay_ms:     150
			jitter_ms:    50
			loss_percent: 2.0
		}
	}
	n8n_route: {
		uses:  "builtin:route"
		label: "n8n Route"
		needs: ["n8n_main", "n8n_main_ready"]
		with: routes: [
			{host: "n8n", address: "${needs.n8n_main.out.host_5678}"},
		]
	}
	// Drives n8n's own REST API (data/seed_n8n.py) to create the instance
	// owner, an AWS credential, and the demo workflow, then publishes it.
	seed_n8n: {
		uses:  "builtin:container"
		label: "Seed n8n"
		needs: ["n8n_main_ready"]
		with: {
			image:      "python:3.12-alpine"
			entrypoint: ["python3"]
			cmd: ["/data/seed_n8n.py"]
			volumes: ["${project.dir}/data:/data:ro"]
		}
	}
	// Polls the workflow's webhook (POST, expecting 200) rather than
	// needing seed_n8n itself: the webhook 404s until seed_n8n activates
	// the workflow, and the workflow's own responseMode is "lastNode", so
	// the first 200 is the AWS nodes having actually finished against
	// MiniStack - the DAG's proof that the whole path works end to end.
	trigger_workflow: {
		uses:  "builtin:wait"
		label: "Trigger Workflow"
		needs: ["seed_n8n", "seed_aws", "n8n_worker_ready", "n8n_main"]
		with: {
			timeout:  "90s"
			interval: "5s"
			http: {
				url:    "http://${needs.n8n_main.out.host_5678}/webhook/run"
				method: "POST"
			}
		}
	}
}

commands: {
	psql: {
		label: "psql"
		needs: ["postgres"]
		run: ["docker", "exec", "-it", "${needs.postgres.out.name}", "psql", "-U", "n8n"]
	}
	trigger: {
		label: "Trigger Workflow"
		needs: ["n8n_main"]
		run: ["curl", "-s", "-X", "POST", "http://${needs.n8n_main.out.host_5678}/webhook/run"]
	}
}
