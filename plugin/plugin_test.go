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
			name: "an intercept route is plain text - it's not a single address to browse to",
			give: Route{Host: "s3.amazonaws.com", Upstream: "127.0.0.1:9000", Intercept: &RouteIntercept{Ports: []int{443}}},
			want: Detail{Value: String("s3.amazonaws.com"), Copyable: true},
		},
		{
			name: "a wildcard host is plain text for the same reason",
			give: Route{Host: "*.myapp.kevin.test", Upstream: "myapp:8080"},
			want: Detail{Value: String("*.myapp.kevin.test"), Copyable: true},
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
