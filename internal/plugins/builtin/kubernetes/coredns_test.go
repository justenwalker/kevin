package kubernetes

import (
	"context"
	"errors"
	"io"
	"slices"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/justenwalker/kevin/plugin"
)

func TestWantsCoreDNSPatch(t *testing.T) {
	tests := []struct {
		name    string
		coredns bool
		relay   string
		domain  string
		want    bool
	}{
		{name: "relay and domain are both set", coredns: true, relay: "10.244.0.5:53", domain: "kevin.home", want: true},
		{name: "the relay is disabled for the environment", coredns: true, relay: "", domain: "kevin.home", want: false},
		{name: "the environment declares no domain", coredns: true, relay: "10.244.0.5:53", domain: "", want: false},
		{name: "the step opts out", coredns: false, relay: "10.244.0.5:53", domain: "kevin.home", want: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := wantsCoreDNSPatch(config{CoreDNS: tt.coredns},
				plugin.Env{Relay: tt.relay, Domain: tt.domain})
			assert.Equal(t, tt.want, got)
		})
	}
}

// patchCoreDNSDriver builds a fakeDriver that walks patchCoreDNS's call
// sequence (get Corefile, render configmap, replace it, point every node's
// DNS at the relay, restart coredns, wait for the rollout) and fails on the
// callN'th call, or never when callN is 0.
func patchCoreDNSDriver(callN int) fakeDriver {
	call := 0
	fails := func() bool {
		call++
		return call == callN
	}
	return fakeDriver{
		kubectl: func(_ context.Context, args ...string) (string, error) {
			if fails() {
				return "", errors.New("exec failed")
			}
			if slices.Contains(args, "get") {
				return ".:53 {\n    forward . 8.8.8.8\n}\n", nil
			}
			return "", nil
		},
		kubectlInput: func(context.Context, io.Reader, ...string) (string, error) {
			if fails() {
				return "", errors.New("exec failed")
			}
			return "", nil
		},
		pointDNS: func(context.Context, []string, string) error {
			if fails() {
				return errors.New("point demo-cluster-control-plane's dns at the relay")
			}
			return nil
		},
	}
}

// customDNSDriver is a fakeDriver that reports a cluster with its own coredns
// configmap.
type customDNSDriver struct{ fakeDriver }

func (customDNSDriver) CoreDNSCustom() {}

func TestPatchCoreDNS(t *testing.T) {
	nodes := []string{"demo-cluster-control-plane"}

	t.Run("happy path patches the Corefile, DNS, and restarts coredns", func(t *testing.T) {
		out := &capture{}
		err := patchCoreDNS(t.Context(), patchCoreDNSDriver(0), nodes, "kevin.home", "10.244.0.5", out)
		require.NoError(t, err)
		assert.Contains(t, strings.Join(out.stdout, "\n"), "coredns resolves *.kevin.home")
	})

	t.Run("hands the nodes and relay to the driver", func(t *testing.T) {
		var gotNodes []string
		var gotRelay string
		drv := patchCoreDNSDriver(0)
		drv.pointDNS = func(_ context.Context, nodes []string, relay string) error {
			gotNodes, gotRelay = nodes, relay
			return nil
		}

		require.NoError(t, patchCoreDNS(t.Context(), drv, nodes, "kevin.home", "10.244.0.5", &capture{}))
		assert.Equal(t, nodes, gotNodes)
		assert.Equal(t, "10.244.0.5", gotRelay)
	})

	t.Run("a cluster that manages coredns gets the zone in coredns-custom", func(t *testing.T) {
		var kubectlCalls [][]string
		var applied []string
		drv := customDNSDriver{fakeDriver{
			kubectl: func(_ context.Context, args ...string) (string, error) {
				kubectlCalls = append(kubectlCalls, args)
				return "manifest", nil
			},
			kubectlInput: func(_ context.Context, _ io.Reader, args ...string) (string, error) {
				applied = args
				return "", nil
			},
		}}

		require.NoError(t, patchCoreDNS(t.Context(), drv, nodes, "kevin.home", "10.244.0.5", &capture{}))

		require.NotEmpty(t, kubectlCalls)
		assert.Equal(t, []string{"create", "configmap", "coredns-custom"}, kubectlCalls[0][:3])
		assert.Contains(t, kubectlCalls[0][5], "--from-literal=kevin.server=")
		assert.Contains(t, kubectlCalls[0][5], "kevin.home:53 {")
		assert.Equal(t, []string{"-n", "kube-system", "apply", "-f", "-"}, applied)
		for _, call := range kubectlCalls {
			assert.NotContains(t, call, "get", "the coredns configmap is not read or replaced")
		}
	})

	for i, step := range []string{
		"read the coredns Corefile",
		"render the coredns configmap",
		"replace the coredns configmap",
		"point demo-cluster-control-plane's dns at the relay",
		"restart coredns",
		"wait for coredns",
	} {
		t.Run(step+" failing propagates", func(t *testing.T) {
			err := patchCoreDNS(t.Context(), patchCoreDNSDriver(i+1), nodes, "kevin.home", "10.244.0.5", &capture{})
			require.Error(t, err)
			assert.Contains(t, err.Error(), step)
		})
	}
}
