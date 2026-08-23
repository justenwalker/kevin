package kind

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
)

// defaultBlock is the server block that kubeadm ships in the coredns
// ConfigMap. Every case below must leave it untouched.
const defaultBlock = `.:53 {
    errors
    health {
       lameduck 5s
    }
    ready
    kubernetes cluster.local in-addr.arpa ip6.arpa {
       pods insecure
       fallthrough in-addr.arpa ip6.arpa
       ttl 30
    }
    prometheus :9153
    forward . /etc/resolv.conf {
       max_concurrent 1000
    }
    cache 30 {
       disable success cluster.local
       disable denial cluster.local
    }
    loop
    reload
    loadbalance
}`

func TestCorefileWithZone(t *testing.T) {
	tests := []struct {
		name     string
		corefile string
		domain   string
		relay    string
	}{
		{
			name:     "no kevin zone",
			corefile: defaultBlock + "\n",
			domain:   "kevin.home",
			relay:    "10.244.0.5:53",
		},
		{
			name: "an existing kevin zone with a different relay",
			corefile: defaultBlock + "\n" +
				"kevin.home:53 {\n    errors\n    cache 30\n    forward . 10.244.0.9:53\n}\n",
			domain: "kevin.home",
			relay:  "10.244.0.5:53",
		},
		{
			name: "unusual whitespace",
			corefile: ".:53 {\n\terrors\n\thealth\n}\n\n" +
				"kevin.home:53    {\n\t\terrors\n\t\tforward . 10.244.0.9:53\n   }\n",
			domain: "kevin.home",
			relay:  "10.244.0.5:53",
		},
		{
			name:     "empty corefile",
			corefile: "",
			domain:   "kevin.home",
			relay:    "10.244.0.5:53",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := corefileWithZone(tt.corefile, tt.domain, tt.relay)

			assert.Equal(t, 1, strings.Count(got, tt.domain+":53 {"),
				"a repeat call must replace the zone, not add a second one")
			assert.Contains(t, got, "forward . "+tt.relay,
				"the new zone must forward to the given relay")
			assert.NotContains(t, got, "10.244.0.9:53",
				"a stale relay address must not survive a replace")

			if strings.Contains(tt.corefile, ".:53 {") {
				assert.Contains(t, got, ".:53 {", "the original zone must survive untouched")
			}
		})
	}

	t.Run("keeps the original block exactly", func(t *testing.T) {
		corefile := defaultBlock + "\n"

		got := corefileWithZone(corefile, "kevin.home", "10.244.0.5:53")

		assert.Contains(t, got, defaultBlock, "the untouched zone must appear byte for byte")
	})

	t.Run("is idempotent", func(t *testing.T) {
		corefile := defaultBlock + "\n"

		once := corefileWithZone(corefile, "kevin.home", "10.244.0.5:53")
		twice := corefileWithZone(once, "kevin.home", "10.244.0.5:53")

		assert.Equal(t, once, twice, "patching an already patched Corefile must change nothing")
	})
}

func TestZoneHeaderNames(t *testing.T) {
	tests := []struct {
		name   string
		header string
		domain string
		want   bool
	}{
		{name: "an exact match with a port", header: "kevin.home:53 {", domain: "kevin.home", want: true},
		{name: "the root zone", header: ".:53 {", domain: "kevin.home", want: false},
		{name: "a different domain", header: "example.com:53 {", domain: "kevin.home", want: false},
		{name: "one of several zones on the header", header: "kevin.home:53 in-addr.arpa:53 {", domain: "kevin.home", want: true},
		{name: "extra whitespace before the brace", header: "kevin.home:53    {", domain: "kevin.home", want: true},
		{name: "a domain that is only a prefix", header: "kevin.home.evil:53 {", domain: "kevin.home", want: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, zoneHeaderNames(tt.header, tt.domain))
		})
	}
}
