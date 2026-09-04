// A kevin environment that fakes out the real AWS S3 endpoints with a
// local emulator (MiniStack, https://ministack.org/), a free, open-source
// AWS emulator similar to LocalStack.
//
//	kevin -C examples/intercept run
//
// fake_s3 starts a MiniStack container. s3_intercept registers both
// s3.us-east-1.amazonaws.com and *.s3.us-east-1.amazonaws.com - real AWS
// hostnames, not subdomains of the environment - as routes into fake_s3,
// using external: true. Both are needed: aws-cli's regional S3 endpoint
// serves a path-style API off the bare domain and a virtual-hosted-style
// API off a per-bucket subdomain. probe then runs the real aws-cli,
// completely unmodified - no --endpoint-url, no local config - to create
// a bucket, upload a file, list it, and read it back. kevin's interception
// is the only reason any of that lands on fake_s3 instead of real AWS.
//
// Reach it from the host the same way:
//
//	curl --proxy http://127.0.0.1:18080 https://s3.us-east-1.amazonaws.com/
//
// Without the CA trusted, curl needs the authority of the project directly:
//
//	curl --proxy http://127.0.0.1:18080 \
//	     --cacert .kevin/ca.crt https://s3.us-east-1.amazonaws.com/
//
// Run `kevin ca install` once for the machine to drop the --cacert flag -
// see the quickstart's "Trust the CA" section.

project: "intercept-example"

proxy: {
	listen:       "127.0.0.1:18080"
	gateway_port: 18082
	egress: deny: true
}
console: listen: "127.0.0.1:18081"

env: {
	fake_s3: {
		uses:  "builtin:container"
		label: "Fake S3 (MiniStack)"
		with: {
			image:  "ministackorg/ministack"
			expose: s3: {port: 4566}
		}
	}
	// fake_s3's port accepts a TCP connection before its HTTP server is
	// actually serving, so wait for a real response before routing
	// anything to it.
	fake_s3_ready: {
		uses:  "builtin:wait"
		label: "S3 Ready"
		needs: ["fake_s3"]
		with: {
			timeout: "15s"
			http: url: "http://${needs.fake_s3.out.host_4566}/"
		}
	}
	// s3_intercept makes a request meant for the real S3 endpoints land on
	// fake_s3 instead of the real internet. external skips the
	// environment-domain suffix a plain route entry would otherwise get:
	// host is used exactly as a client already dials it.
	s3_intercept: {
		uses:  "builtin:route"
		label: "Intercept S3"
		needs: ["fake_s3", "fake_s3_ready"]
		with: routes: [
			{host: "s3.us-east-1.amazonaws.com", address: "${needs.fake_s3.out.host_4566}", external: true},
			{host: "*.s3.us-east-1.amazonaws.com", address: "${needs.fake_s3.out.host_4566}", external: true},
		]
	}
	// probe runs the real aws-cli, completely unmodified - no
	// --endpoint-url, no local config. It targets the same hostnames a
	// real client would; kevin's interception is the only reason it lands
	// on fake_s3. AWS_CA_BUNDLE points aws-cli at the CA that kevin's
	// proxy signs its leaf certificates with - the same certificate every
	// builtin:container step mounts at this path, kevin's proxy MITMs
	// every request, but aws-cli validates certificates against its own
	// bundle rather than the system store SSL_CERT_FILE covers.
	probe: {
		uses:  "builtin:container"
		label: "Probe (through proxy)"
		needs: ["fake_s3", "s3_intercept"]
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
				echo hello from kevin > /tmp/hello.txt
				echo '--- aws s3 mb: create the bucket ---'
				aws s3 mb s3://kevin-example
				echo '--- aws s3 cp: upload a file ---'
				aws s3 cp /tmp/hello.txt s3://kevin-example/hello.txt
				echo '--- aws s3 ls: list the bucket ---'
				aws s3 ls s3://kevin-example
				echo '--- aws s3 cp: read the file back ---'
				aws s3 cp s3://kevin-example/hello.txt -
				sleep 3600
				"""]
		}
	}
}
