// Package docker implements [container.Runtime] using docker.
package docker

import (
	"github.com/justenwalker/kevin/internal/container"
	"github.com/justenwalker/kevin/internal/container/clirt"
)

// flavor is the docker [clirt.Dialect].
type flavor struct{}

// Dialect implements [clirt.Flavor].
func (flavor) Dialect() clirt.Dialect {
	return clirt.Dialect{
		Binary:           "docker",
		VersionTemplate:  "{{.ServerVersion}}",
		GatewayTemplate:  "{{range .IPAM.Config}}{{.Gateway}} {{end}}",
		DisableIPv6:      true,
		NotInstalledHint: "docker isn't installed, or isn't on PATH",
		NotRunningHint:   "Docker isn't running - start Docker Desktop (or dockerd), then retry",
		PortInUse:        []string{"port is already allocated", "address already in use"},
		ImageMissing:     []string{"manifest unknown", "pull access denied", "repository does not exist"},
	}
}

// Client runs docker commands. The zero value is ready to use.
type Client struct {
	clirt.Client[flavor]
}

var _ container.Runtime = Client{}
