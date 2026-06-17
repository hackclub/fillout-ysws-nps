package nps

import (
	"regexp"
	"strings"
)

// tagInvalid matches runs of characters not allowed in a tag. Tags are single
// words of letters, digits, dashes, and underscores.
var tagInvalid = regexp.MustCompile(`[^A-Za-z0-9_-]+`)

// NormalizeTags parses a comma-separated tag string into clean, de-duplicated
// tags. Each tag is reduced to a single word: invalid character runs (including
// spaces) become underscores, and leading/trailing separators are trimmed.
// Example: "2026-06-15 2nd newsletter, Newsletter" -> ["2026-06-15_2nd_newsletter", "Newsletter"].
func NormalizeTags(input string) []string {
	seen := make(map[string]bool)
	var out []string
	for _, part := range strings.Split(input, ",") {
		tag := tagInvalid.ReplaceAllString(strings.TrimSpace(part), "_")
		tag = strings.Trim(tag, "_-")
		if tag == "" || seen[strings.ToLower(tag)] {
			continue
		}
		seen[strings.ToLower(tag)] = true
		out = append(out, tag)
	}
	return out
}
