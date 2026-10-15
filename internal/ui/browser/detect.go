package browser

import "strings"

// Kind identifies a browser family, detected from a User-Agent string.
type Kind int

// The browser kinds Detect recognizes.
const (
	Unknown Kind = iota
	Firefox
	// Chrome covers every Chromium-based browser (Chrome, Edge, Brave,
	// Opera, Chrome on iOS), since they all share the same proxy setup.
	Chrome
	Safari
)

// String returns the kind's name, or "Unknown".
func (k Kind) String() string {
	switch k {
	case Unknown:
		return "Unknown"
	case Firefox:
		return "Firefox"
	case Chrome:
		return "Chrome"
	case Safari:
		return "Safari"
	default:
		return "Unknown"
	}
}

// Detect returns the browser kind a User-Agent header identifies. Chrome's
// own User-Agent still contains "Safari/...", so Chrome (and other
// Chromium-based browsers) must be matched before Safari.
func Detect(userAgent string) Kind {
	switch {
	case strings.Contains(userAgent, "Firefox/"):
		return Firefox
	case strings.Contains(userAgent, "Chrome/"),
		strings.Contains(userAgent, "CriOS/"),
		strings.Contains(userAgent, "Chromium/"):
		return Chrome
	case strings.Contains(userAgent, "Safari/"):
		return Safari
	default:
		return Unknown
	}
}
