package formatting

import (
	"fmt"
	"runtime"
	"strings"
	"testing"
)

func htmlReconstructionBomb(count int) string {
	var input strings.Builder
	input.WriteString("<p>")
	for i := 0; i < count; i++ {
		// Distinct attributes defeat HTML5's cap on identical formatting entries.
		fmt.Fprintf(&input, "<b id=%d>", i)
	}
	input.WriteString(strings.Repeat("<p>x", count))
	return input.String()
}

// Without neutralization, the first bomb allocated ~641 MB of cloned nodes.
func requireBoundedHTMLConversion(t *testing.T, input string) {
	t.Helper()
	if len(input) > maxHTMLBytes || strings.Count(input, "<") > maxHTMLMarkup {
		t.Fatal("regression must fit within the source-size limits")
	}
	for _, convert := range []func(string) (string, error){HTMLToTelegramText, StripHTML} {
		runtime.GC()
		var before, after runtime.MemStats
		runtime.ReadMemStats(&before)
		_, err := convert(input)
		runtime.ReadMemStats(&after)
		if err != nil {
			t.Fatal(err)
		}
		if allocated := after.TotalAlloc - before.TotalAlloc; allocated > 16<<20 {
			t.Fatalf("HTML conversion allocated %d bytes (limit 16 MiB)", allocated)
		}
	}
}

func TestHTMLFormattingReconstructionIsBounded(t *testing.T) {
	bomb := htmlReconstructionBomb(2000)
	for _, tt := range []struct{ name, input string }{
		{"distinct formatting", bomb},
		{"anchors", strings.ReplaceAll(bomb, "<b ", "<a ")},
		{"attribute slices", "<p><b " + strings.Repeat("x ", 1000) + ">" + strings.Repeat("<p>x", 2000)},
		{"anchor attribute slices", "<p><a " + strings.Repeat("x ", 1000) + ">" + strings.Repeat("</p><p>x", 1500)},
		{"adoption agency", strings.Repeat("<b><div>", 1000) + strings.Repeat("</b>", 1000)},
		{"many incomplete tags", strings.Repeat("<b ", 2700)},
		{"overlong formatting tag", `<b title="` + strings.Repeat("x", 1<<20) + `">Body</b>`},
		{"uppercase", strings.ToUpper(bomb)},
		{"self closing", strings.ReplaceAll(bomb, ">", "/>")},
		{"form feed", strings.ReplaceAll(bomb, " id=", "\fid=")},
		{"foreign raw text", "<svg><style>" + bomb},
		{"raw text comment ambiguity", "<script><!--</script>" + bomb},
		{"noscript", "<noscript/>" + bomb},
		{"inside attribute", `<div title="` + bomb + `">Body</div>`},
		{"inside comment", "<!--" + bomb + "-->Body"},
		{"quoted greater than", `<p><b title=">" ` + strings.Repeat("x ", 1000) + ">" + strings.Repeat("<p>x", 2000)},
		{"adjacent attributes", `<p><b ` + strings.Repeat(`x=""`, 1000) + ">" + strings.Repeat("<p>x", 2000)},
	} {
		t.Run(tt.name, func(t *testing.T) {
			requireBoundedHTMLConversion(t, tt.input)
		})
	}
	for _, name := range []string{"a", "b", "big", "code", "em", "font", "i", "nobr", "s", "small", "strike", "strong", "tt", "u"} {
		t.Run(name, func(t *testing.T) {
			requireBoundedHTMLConversion(t, strings.ReplaceAll(bomb, "<b ", "<"+name+" "))
		})
	}
}

func TestHTMLFormattingNeutralizedAfterConditionalFiltering(t *testing.T) {
	var input strings.Builder
	input.WriteString("<p>")
	for i := 0; i < 700; i++ {
		fmt.Fprintf(&input, "<<![if false]>discard<![endif]>b id=%d>", i)
	}
	input.WriteString(strings.Repeat("<p>x", 700))
	filtered, err := filterHTMLConditionals(input.String())
	if err != nil || filtered != htmlReconstructionBomb(700) {
		t.Fatal("conditional deletion did not manufacture the expected tags")
	}
	if got := neutralizeHTMLFormattingTags(filtered); strings.Contains(got, "<b") {
		t.Fatal("spliced formatting tags were not neutralized")
	}
	requireBoundedHTMLConversion(t, input.String())
}

func TestNeutralizeHTMLFormattingTags(t *testing.T) {
	for _, tt := range []struct{ input, want string }{
		{"<b>Bold</b>", "<span>Bold</span>"},
		{`<A HREF="https://x">Up</A >`, `</x-gmail-bot-a><x-gmail-bot-a HREF="https://x">Up</x-gmail-bot-a >`},
		{"<strong\fclass=x/><code>c</code>", "<span\fclass=x/><span>c</span>"},
		{"<<b", "<<span"},
		{"</", "</"},
		{"x < b", "x < b"},
		// Longer non-formatting names must not be mistaken for formatting tags.
		{"<body><base><span><image><stronger><bdi></bdi>", "<body><base><span><image><stronger><bdi></bdi>"},
	} {
		if got := neutralizeHTMLFormattingTags(tt.input); got != tt.want {
			t.Errorf("neutralize(%q) = %q, want %q", tt.input, got, tt.want)
		}
	}
}

func TestHTMLFormattingOrdinaryEmail(t *testing.T) {
	row := `<tr><td><b>Label</b></td><td><a href="https://example.com">Item</a></td></tr>`
	input := "<table>" + strings.Repeat(row, 100) + "</table>"
	wantRow := "Label | " + EncodeLinkToken("Item", "https://example.com")
	if got := mustHTMLText(t, input); got != strings.TrimSuffix(strings.Repeat(wantRow+"\n", 100), "\n") {
		t.Fatal("ordinary formatted table lost content")
	}
}

// GitHub release notifications render long changelogs with hundreds of code
// spans and issue links, each with many attributes. These used to exceed the
// conservative formatting-clone budget.
func TestHTMLFormattingGitHubReleaseNotes(t *testing.T) {
	var input strings.Builder
	input.WriteString(`<html><body><h1><a href="https://github.com/o/r/releases/tag/v1.5.0">v1.5.0</a></h1><h2>Fixed</h2><ul>`)
	for i := 0; i < 300; i++ {
		fmt.Fprintf(&input, `<li><strong>Fix</strong> <code>pi.exec()</code> with <code>maxBuffer</code> (<a class="issue-link js-issue-link" data-error-text="Failed to load title" data-id="%d" data-permission-text="Title is private" data-url="https://github.com/o/r/issues/%d" data-hovercard-type="issue" data-hovercard-url="/o/r/issues/%d/hovercard" href="https://github.com/o/r/issues/%d">#%d</a>)</li>`, i, i, i, i, i)
	}
	input.WriteString(`</ul></body></html>`)
	if strings.Count(input.String(), "<") > maxHTMLMarkup {
		t.Fatal("fixture must fit within the source-size limits")
	}
	got := mustHTMLText(t, input.String())
	if !strings.HasPrefix(got, EncodeLinkToken("v1.5.0", "https://github.com/o/r/releases/tag/v1.5.0")+"\n\nFixed\n\n") {
		t.Fatalf("unexpected heading: %.200q", got)
	}
	last := "- Fix pi.exec() with maxBuffer (" + EncodeLinkToken("#299", "https://github.com/o/r/issues/299") + ")"
	if strings.Count(got, "- Fix pi.exec() with maxBuffer (") != 300 || !strings.HasSuffix(got, last) {
		t.Fatalf("release notes lost content (%d bytes)", len(got))
	}
}
