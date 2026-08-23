package kind

import "strings"

// corefileWithZone returns the Corefile with a forward zone for domain
// pointing at relay. An existing zone for the same domain is replaced.
func corefileWithZone(corefile, domain, relay string) string {
	block := zoneBlock(domain, relay)

	lines := strings.Split(corefile, "\n")
	out := make([]string, 0, len(lines)+4)
	replaced := false

	for i := 0; i < len(lines); {
		line := lines[i]
		trimmed := strings.TrimSpace(line)
		if trimmed == "" || !strings.HasSuffix(trimmed, "{") {
			out = append(out, line)
			i++
			continue
		}

		start := i
		depth := strings.Count(line, "{") - strings.Count(line, "}")
		i++
		for depth > 0 && i < len(lines) {
			depth += strings.Count(lines[i], "{") - strings.Count(lines[i], "}")
			i++
		}

		if zoneHeaderNames(trimmed, domain) {
			out = append(out, block)
			replaced = true
			continue
		}
		out = append(out, lines[start:i]...)
	}

	if !replaced {
		result := strings.TrimRight(strings.Join(out, "\n"), "\n")
		if result != "" {
			result += "\n\n"
		}
		return result + block + "\n"
	}
	return strings.TrimRight(strings.Join(out, "\n"), "\n") + "\n"
}

// zoneBlock renders the CoreDNS server block that forwards domain to relay.
func zoneBlock(domain, relay string) string {
	return domain + ":53 {\n" +
		"    errors\n" +
		"    cache 30\n" +
		"    forward . " + relay + "\n" +
		"}"
}

// zoneHeaderNames reports whether a Corefile server block header, such as
// "kevin.home:53 {", names domain among its zones.
func zoneHeaderNames(header, domain string) bool {
	header = strings.TrimSuffix(header, "{")
	for token := range strings.FieldsSeq(header) {
		name, _, _ := strings.Cut(token, ":")
		if name == domain {
			return true
		}
	}
	return false
}
