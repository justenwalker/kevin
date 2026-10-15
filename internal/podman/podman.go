// Package podman implements [cri.Runtime] using podman.
package podman

import (
	"context"
	"fmt"
	"runtime"
	"strings"

	"github.com/justenwalker/kevin/internal/cri"
	"github.com/justenwalker/kevin/internal/cri/clirt"
)

// flavor is the podman [clirt.Dialect].
type flavor struct{}

// Dialect implements [clirt.Flavor]. DisableIPv6 stays off because podman
// creates IPv4-only networks unless --ipv6 is passed.
func (flavor) Dialect() clirt.Dialect {
	return clirt.Dialect{
		Binary:           "podman",
		VersionTemplate:  "{{.Version.Version}}",
		GatewayTemplate:  "{{range .Subnets}}{{.Gateway}} {{end}}",
		NotInstalledHint: "podman isn't installed, or isn't on PATH",
		NotRunningHint:   "podman isn't running - start it (podman machine start on macOS), then retry",
		PortInUse:        []string{"address already in use", "port is already allocated"},
		ImageMissing:     []string{"manifest unknown", "unauthorized", "repository does not exist", "image not known"},
	}
}

// Client runs podman commands. The zero value is ready to use.
type Client struct {
	clirt.Client[flavor]
}

var (
	_ cri.Runtime       = Client{}
	_ cri.DockerAPIHost = Client{}
)

// socket returns the path of the Docker-compatible API socket of the podman
// service. It asks podman info on Linux and the default podman machine
// elsewhere, and returns [ErrNoSocket] when podman names none.
func (c Client) socket(ctx context.Context) (string, error) {
	args := []string{"info", "--format", "{{.Host.RemoteSocket.Path}}"}
	if runtime.GOOS != "linux" {
		args = []string{"machine", "inspect", "--format", "{{.ConnectionInfo.PodmanSocket.Path}}"}
	}
	out, err := c.Output(ctx, nil, args...)
	if err != nil {
		return "", fmt.Errorf("podman: find the API socket: %w", err)
	}
	return socketPath(out)
}

// socketPath extracts the socket path from the output of the podman command
// that Socket runs.
func socketPath(out string) (string, error) {
	path := strings.TrimPrefix(strings.TrimSpace(out), "unix://")
	if path == "" || path == "<no value>" {
		return "", ErrNoSocket
	}
	return path, nil
}

// DockerHost implements [cri.DockerAPIHost] with the unix:// URL of the
// Docker-compatible API socket of the podman service.
func (c Client) DockerHost(ctx context.Context) (string, error) {
	socket, err := c.socket(ctx)
	if err != nil {
		return "", err
	}
	return "unix://" + socket, nil
}
