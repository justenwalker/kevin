package kubernetes

import (
	"encoding/json"
	"fmt"

	"github.com/justenwalker/kevin/internal/cri"
)

// nodeLabelKey is the Kubernetes node label that names each node: the
// control-plane node gets controlPlaneNodeName, a worker its with.workers key.
const nodeLabelKey = cri.LabelPrefix + "node"

// controlPlaneNodeName is the nodeLabelKey value of the control-plane node.
const controlPlaneNodeName = "control-plane"

// config is the decoded with block of one step.
type config struct {
	Driver  string                    `json:"driver"`
	Name    string                    `json:"name"`
	Workers map[string]map[string]any `json:"workers"`
	Wait    string                    `json:"wait"`
	Retain  bool                      `json:"retain"`
	Proxy   bool                      `json:"proxy"`
	Egress  []string                  `json:"egress"`
	CoreDNS bool                      `json:"coredns"`
	TrustCA bool                      `json:"trust_ca"`
	Expose  map[string]expose         `json:"expose"`
	Relay   bool                      `json:"relay"`
	Kind    kindConfig                `json:"kind"`
	K3d     k3dConfig                 `json:"k3d"`
	Mounts  []mount                   `json:"mounts"`
}

// mount is one entry of the with block's mounts list: a host path that
// every node of the cluster sees.
type mount struct {
	Host      string `json:"host"`
	Container string `json:"container"`
	ReadOnly  bool   `json:"readonly"`
}

// resolveMounts returns mounts with each relative host path resolved against
// projectDir.
func resolveMounts(mounts []mount, projectDir string) []mount {
	resolved := make([]mount, len(mounts))
	for i, m := range mounts {
		m.Host = resolvePath(m.Host, projectDir)
		resolved[i] = m
	}
	return resolved
}

// kindConfig is the with block's kind field: the settings that only the
// kind driver has.
type kindConfig struct {
	Image        string         `json:"image"`
	ControlPlane map[string]any `json:"control_plane"`
	Config       string         `json:"config"`
}

// k3dConfig is the with block's k3d field: the settings that only the k3d
// driver has.
type k3dConfig struct {
	Image   string            `json:"image"`
	Disable []string          `json:"disable"`
	Env     map[string]string `json:"env"`
	Memory  string            `json:"memory"`
	Labels  map[string]string `json:"labels"`
}

// expose is one entry of the with block's expose map: an in-cluster
// address to reach through the SOCKS5 relay. The map key is its name.
type expose struct {
	Address  string `json:"address"`
	Protocol string `json:"protocol"`
	HostPort int    `json:"host_port"`
}

// clusterName builds the name of the cluster.
func clusterName(cfg config, project, step string) string {
	if cfg.Name != "" {
		return cfg.Name
	}
	return project + "-" + step
}

func decode(data []byte) (config, error) {
	cfg := config{Wait: "5m", Proxy: true, CoreDNS: true, TrustCA: true}
	if len(data) == 0 {
		return cfg, nil
	}
	if err := json.Unmarshal(data, &cfg); err != nil {
		return cfg, fmt.Errorf("kubernetes: decode config: %w", err)
	}
	// Repeat schema.cue's per-entry default, for a caller that bypasses CUE.
	for name, e := range cfg.Expose {
		if e.Protocol == "" {
			e.Protocol = "tcp"
			cfg.Expose[name] = e
		}
	}
	return cfg, nil
}

// proxyEnv reports the proxy variables to set in the nodes, or nil when
// the step's with block turns the proxy off, or the environment has none
// configured.
func proxyEnv(cfg config, procEnv map[string]string) map[string]string {
	if !cfg.Proxy || len(procEnv) == 0 {
		return nil
	}
	return procEnv
}
