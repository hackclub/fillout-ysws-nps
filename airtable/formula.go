package airtable

import "strings"

// QuoteString renders s as an Airtable formula string literal: it wraps s in
// double quotes and escapes backslashes and double quotes. This prevents a value
// from terminating the literal early or injecting formula syntax. Use it for
// every untrusted value interpolated into a filterByFormula, e.g.:
//
//	fmt.Sprintf("FIND(%s, {Notes})", airtable.QuoteString(needle))
func QuoteString(s string) string {
	return `"` + strings.NewReplacer(`\`, `\\`, `"`, `\"`).Replace(s) + `"`
}
