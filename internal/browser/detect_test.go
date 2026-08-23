package browser_test

import (
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/justenwalker/kevin/internal/browser"
)

func TestDetect(t *testing.T) {
	tests := []struct {
		name string
		ua   string
		want browser.Kind
	}{
		{
			name: "Firefox desktop",
			ua:   "Mozilla/5.0 (Macintosh; Intel Mac OS X 10.15; rv:128.0) Gecko/20100101 Firefox/128.0",
			want: browser.Firefox,
		},
		{
			name: "Chrome desktop",
			ua:   "Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/126.0.0.0 Safari/537.36",
			want: browser.Chrome,
		},
		{
			name: "Safari desktop",
			ua:   "Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/605.1.15 (KHTML, like Gecko) Version/17.5 Safari/605.1.15",
			want: browser.Safari,
		},
		{
			name: "Chrome on iOS",
			ua:   "Mozilla/5.0 (iPhone; CPU iPhone OS 17_5 like Mac OS X) AppleWebKit/605.1.15 (KHTML, like Gecko) CriOS/126.0.6478.54 Mobile/15E148 Safari/604.1",
			want: browser.Chrome,
		},
		{
			name: "curl, no browser",
			ua:   "curl/8.7.1",
			want: browser.Unknown,
		},
		{
			name: "empty",
			ua:   "",
			want: browser.Unknown,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, browser.Detect(tt.ua))
		})
	}
}

func TestKindString(t *testing.T) {
	assert.Equal(t, "Firefox", browser.Firefox.String())
	assert.Equal(t, "Chrome", browser.Chrome.String())
	assert.Equal(t, "Safari", browser.Safari.String())
	assert.Equal(t, "Unknown", browser.Unknown.String())
}
