package main

import "testing"

func TestRenderRichText(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want string
	}{
		{
			name: "bold label then plain answer, one line",
			in:   "**What was the best thing about Hack Club: The Game?** Everything",
			want: "<p><strong>What was the best thing about Hack Club: The Game?</strong> Everything</p>",
		},
		{
			name: "two templated answers concatenated become two paragraphs",
			in:   "**Q1** A1\n\n**Q2** A2",
			want: "<p><strong>Q1</strong> A1</p><p><strong>Q2</strong> A2</p>",
		},
		{
			name: "italic dedup stamp",
			in:   "_Fillout Submission: c8158fac-69b8-4a81-a53c-457422f24c81_",
			want: "<p><em>Fillout Submission: c8158fac-69b8-4a81-a53c-457422f24c81</em></p>",
		},
		{
			name: "label on its own line then answer uses a line break",
			in:   "**Question?**\nthe answer",
			want: "<p><strong>Question?</strong><br>the answer</p>",
		},
		{
			name: "blank input renders nothing",
			in:   "   ",
			want: "",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := string(renderRichText(tc.in)); got != tc.want {
				t.Errorf("renderRichText(%q) =\n%q\nwant\n%q", tc.in, got, tc.want)
			}
		})
	}
}

// User-submitted answers must never be able to inject HTML/script; the raw text
// is escaped before our Markdown formatting is applied.
func TestRenderRichTextEscapesUserContent(t *testing.T) {
	got := string(renderRichText("**Q** <script>alert('x')</script> & <b>nope</b>"))
	want := "<p><strong>Q</strong> &lt;script&gt;alert(&#39;x&#39;)&lt;/script&gt; &amp; &lt;b&gt;nope&lt;/b&gt;</p>"
	if got != want {
		t.Errorf("renderRichText escaping =\n%q\nwant\n%q", got, want)
	}
}
