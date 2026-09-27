// Package varfile reads a flat "KEY=VALUE" file, the file source for
// kevin.cue's declared "variables:" block.
package varfile

import (
	"bufio"
	"fmt"
	"io"
	"os"
	"strings"
)

// ParseFile reads and parses path.
func ParseFile(path string) (map[string]string, error) {
	f, err := os.Open(path) //nolint:gosec // path comes from a user-supplied --var-file flag, the same trust level as kevin.cue itself
	if err != nil {
		return nil, fmt.Errorf("varfile: open %s: %w", path, err)
	}
	defer func() { _ = f.Close() }()

	values, err := Parse(f)
	if err != nil {
		return nil, fmt.Errorf("varfile: %s: %w", path, err)
	}
	return values, nil
}

// Parse reads one "KEY=VALUE" pair per line from r. A blank line or a line
// whose first non-space character is "#" is skipped. Leading and trailing
// space around both the key and the value is trimmed. A line with no "="
// is an error.
func Parse(r io.Reader) (map[string]string, error) {
	values := map[string]string{}
	scanner := bufio.NewScanner(r)
	for lineNum := 1; scanner.Scan(); lineNum++ {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		key, value, ok := strings.Cut(line, "=")
		if !ok {
			return nil, fmt.Errorf("varfile: line %d: missing \"=\": %q", lineNum, line)
		}
		values[strings.TrimSpace(key)] = strings.TrimSpace(value)
	}
	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("varfile: read: %w", err)
	}
	return values, nil
}
