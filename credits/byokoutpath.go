package credits

import "strings"

// outboundPath resolves the final upstream path: provider base path (if any)
// plus the rest of the BYOK path after the provider segment. If the base
// already carries its own trailing segment (e.g. openai base ends in /v1),
// avoid doubling it.
func outboundPath(basePath, rest string) string {
	base := strings.Trim(strings.TrimSuffix(basePath, "/"), "/")
	rest = strings.Trim(rest, "/")
	if rest == "" {
		if base == "" {
			return "/"
		}
		return "/" + base
	}
	if base != "" {
		first := strings.SplitN(rest, "/", 2)[0]
		if strings.HasSuffix(base, first) {
			base = strings.TrimSuffix(strings.TrimSuffix(base, "/"), first)
		}
	}
	if base == "" {
		return "/" + rest
	}
	return "/" + base + "/" + rest
}
