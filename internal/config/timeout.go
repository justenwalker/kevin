package config

import (
	"fmt"
	"time"
)

// Timeout is how long a step's Up may run. Zero means no limit.
type Timeout time.Duration

// UnmarshalText decodes a positive duration such as "2m".
func (t *Timeout) UnmarshalText(text []byte) error {
	d, err := time.ParseDuration(string(text))
	if err != nil || d <= 0 {
		return fmt.Errorf("%w: %q", ErrBadTimeout, string(text))
	}
	*t = Timeout(d)
	return nil
}
