package airtable

import "testing"

func TestQuoteString(t *testing.T) {
	cases := map[string]string{
		"plain":     `"plain"`,
		"":          `""`,
		`a"b`:       `"a\"b"`,
		`a\b`:       `"a\\b"`,
		`") OR 1=1`: `"\") OR 1=1"`,
	}
	for in, want := range cases {
		if got := QuoteString(in); got != want {
			t.Errorf("QuoteString(%q) = %q, want %q", in, got, want)
		}
	}
}
