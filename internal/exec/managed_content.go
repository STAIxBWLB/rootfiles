package exec

import "strings"

// ManagedContentEqual treats the previous generated-file marker as the current
// marker. Everything else remains byte-exact, including suffixes and body text.
// Only the first comment (or the comment immediately after a shebang) qualifies.
func ManagedContentEqual(got, want string) bool {
	return got == want || canonicalManagedHeader(got) == canonicalManagedHeader(want)
}

func canonicalManagedHeader(text string) string {
	start := 0
	if strings.HasPrefix(text, "#!") {
		end := strings.IndexByte(text, '\n')
		if end < 0 {
			return text
		}
		start = end + 1
	}
	for _, prefix := range []string{"# Managed by rootfiles", "// Managed by rootfiles"} {
		legacy := prefix + "-v2"
		if !strings.HasPrefix(text[start:], legacy) {
			continue
		}
		end := start + len(legacy)
		if end == len(text) || strings.ContainsRune(" \t\r\n:", rune(text[end])) {
			return text[:start] + prefix + text[end:]
		}
	}
	return text
}
