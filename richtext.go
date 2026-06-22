package main

import (
	"html/template"
	"regexp"
	"strings"
)

// The NPS rich-text fields (the feedback columns and the Custom Fields catch-all)
// embed a small amount of Markdown: **bold** question labels, the _italic_ dedup
// stamp, and blank-line-separated paragraphs. Airtable renders these as formatted
// rich text, so the preview does the same to match the real cell.
var (
	paragraphSplit = regexp.MustCompile(`\n[ \t]*\n`)
	boldSpan       = regexp.MustCompile(`\*\*(.+?)\*\*`)
	italicSpan     = regexp.MustCompile(`_(.+?)_`)
)

// renderRichText converts our embedded Markdown into the HTML an Airtable
// rich-text long-text field displays. The input is HTML-escaped before any
// formatting is applied, so user-submitted answers can never inject markup.
func renderRichText(s string) template.HTML {
	s = strings.ReplaceAll(s, "\r\n", "\n")

	var b strings.Builder
	for _, para := range paragraphSplit.Split(s, -1) {
		if strings.TrimSpace(para) == "" {
			continue
		}
		b.WriteString("<p>")
		b.WriteString(renderInline(para))
		b.WriteString("</p>")
	}
	return template.HTML(b.String())
}

// renderInline escapes one paragraph and applies inline formatting: bold first
// (so its ** markers are consumed before italics), then italics, then single
// newlines become line breaks.
func renderInline(para string) string {
	esc := template.HTMLEscapeString(para)
	esc = boldSpan.ReplaceAllString(esc, "<strong>$1</strong>")
	esc = italicSpan.ReplaceAllString(esc, "<em>$1</em>")
	return strings.ReplaceAll(esc, "\n", "<br>")
}
