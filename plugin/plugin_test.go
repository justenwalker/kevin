package plugin

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestDetail(t *testing.T) {
	tests := []struct {
		name string
		give interface{ Detail() Detail }
		want Detail
	}{
		{
			name: "Route is a copyable link to the host",
			give: Route{Host: "api.test", Upstream: "api:8080", TLS: true},
			want: Detail{Value: String("api.test"), Href: "https://api.test", Copyable: true},
		},
		{
			name: "ExposedPort labels protocol and name",
			give: ExposedPort{Name: "postgres", Protocol: "tcp", Upstream: "127.0.0.1:54321"},
			want: Detail{Label: "tcp postgres", Value: String("127.0.0.1:54321"), Copyable: true},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, tt.give.Detail())
		})
	}
}
