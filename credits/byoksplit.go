package credits

import "strings"

// splitPath returns (provider, restPath) from a path like
// /api/byok/openai/v1/chat/completions → ("openai", "v1/chat/completions").
func splitPath(p string) (string, string) {
	t := strings.Trim(p, "/")
	// expected prefix: api/byok/<provider>/<rest...>
	const maxSegments = 4
	parts := strings.SplitN(t, "/", maxSegments)
	if len(parts) < 3 || parts[0] != "api" || parts[1] != "byok" || parts[2] == "" {
		return "", ""
	}
	if len(parts) == 3 {
		return parts[2], ""
	}
	return parts[2], parts[3]
}
