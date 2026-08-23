//go:build e2e

package e2e

import (
	"net/http"
	"os"
	"path/filepath"
	"syscall"
	"testing"

	"github.com/stretchr/testify/suite"
)

// interceptCUE mirrors examples/intercept/kevin.cue: fake_s3 (MiniStack)
// stands in for the real AWS S3 endpoints, s3_intercept registers both the
// bare and wildcard regional hostnames as external: true routes, and probe
// runs the real, unmodified aws-cli against those hostnames.
const interceptCUE = `project: "%s"

env: {
	fake_s3: {
		uses:  "builtin:container"
		label: "Fake S3 (MiniStack)"
		with: {
			image:  "ministackorg/ministack"
			expose: [{port: 4566}]
		}
	}
	fake_s3_ready: {
		uses:  "builtin:wait"
		label: "S3 Ready"
		needs: ["fake_s3"]
		with: {
			timeout: "15s"
			http: url: "http://${needs.fake_s3.out.host_4566}/"
		}
	}
	s3_intercept: {
		uses:  "builtin:route"
		label: "Intercept S3"
		needs: ["fake_s3", "fake_s3_ready"]
		with: routes: [
			{host: "s3.us-east-1.amazonaws.com", address: "${needs.fake_s3.out.host_4566}", external: true},
			{host: "*.s3.us-east-1.amazonaws.com", address: "${needs.fake_s3.out.host_4566}", external: true},
		]
	}
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
`

// InterceptSuite covers docs/MANUAL_TESTING.md section 8: builtin:route
// with external: true.
type InterceptSuite struct {
	e2eSuite
}

func TestInterceptSuite(t *testing.T) {
	suite.Run(t, new(InterceptSuite))
}

func (s *InterceptSuite) TestIntercept() {
	s.requireDocker()

	const project = "kevin-e2e-intercept"
	dir := s.project(project, interceptCUE)

	p := s.startKevin(dir, "-C", dir, "run")
	s.waitFor(p, stepLine("probe", "ready"), 3*defaultTimeout)
	out := p.buf.String()

	m := addrRE.FindAllStringSubmatch(out, -1)
	var proxyAddr string
	for _, row := range m {
		if row[1] == "proxy" {
			proxyAddr = row[2]
		}
	}
	s.Require().NotEmpty(proxyAddr, "output:\n%s", out)

	s.T().Cleanup(func() {
		require := s.Require()
		require.NoError(p.cmd.Process.Signal(syscall.SIGINT))
		s.waitExit(p, defaultTimeout)
	})

	// probe runs the real, unmodified aws-cli against the real AWS
	// hostnames - kevin's interception is the only reason any of it lands
	// on fake_s3. The final read-back's own stdout ("hello from kevin") is
	// the unique marker that every step (mb, cp, ls, and the read-back)
	// succeeded, since the earlier "echo ... > /tmp/hello.txt" writes to a
	// file rather than stdout.
	logs := s.waitDockerLogs("kevin-"+project+"-probe", "hello from kevin", defaultTimeout)
	s.Contains(logs, "aws s3 mb: create the bucket")
	s.Contains(logs, "aws s3 cp: upload a file")
	s.Contains(logs, "aws s3 ls: list the bucket")
	s.Contains(logs, "aws s3 cp: read the file back")

	// From the host: the interception isn't container-only.
	pem, err := os.ReadFile(filepath.Join(dir, ".kevin", "root.crt"))
	s.Require().NoError(err)
	client := proxyHTTPClient(proxyAddr, newCertPool(pem))

	resp := httpGet(s.T(), client, "https://s3.us-east-1.amazonaws.com/")
	defer resp.Body.Close() //nolint:errcheck // read-only response body
	s.NotEqual(http.StatusForbidden, resp.StatusCode, "the host-side request must also land on the fake, not get egress-denied")
}
