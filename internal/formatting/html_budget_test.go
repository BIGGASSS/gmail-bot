package formatting

import (
	"errors"
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

func TestHTMLCloneBudgetBeforeDOMAllocation(t *testing.T) {
	for _, tt := range []struct{ name, input string }{
		{"distinct formatting", htmlReconstructionBomb(2000)},
		{"attribute slices", "<p><b " + strings.Repeat("x ", 1000) + ">" + strings.Repeat("<p>x", 2000)},
		{"many incomplete tags", strings.Repeat("<b ", 2700)},
		{"overlapping incomplete tags", strings.Repeat("<b ", 100) + strings.Repeat("x ", 3000)},
		{"overlong formatting tag", `<b title="` + strings.Repeat("x", maxHTMLFormattingTagBytes) + `">Body</b>`},
	} {
		t.Run(tt.name, func(t *testing.T) {
			if len(tt.input) > maxHTMLBytes || strings.Count(tt.input, "<") > maxHTMLMarkup {
				t.Fatal("regression must fit within the source-size limits")
			}
			for _, convert := range []func(string) (string, error){HTMLToTelegramText, StripHTML} {
				runtime.GC()
				var before, after runtime.MemStats
				runtime.ReadMemStats(&before)
				got, err := convert(tt.input)
				runtime.ReadMemStats(&after)
				if !errors.Is(err, ErrHTMLResourceLimit) || got != "" {
					t.Fatalf("got %d bytes, error %v; want no text and resource limit error", len(got), err)
				}
				// The first case previously allocated ~641 MB. Preflight itself
				// must also remain bounded, even for overlapping EOF candidates.
				if allocated := after.TotalAlloc - before.TotalAlloc; allocated > 16<<20 {
					t.Fatalf("rejected HTML allocated %d bytes (limit 16 MiB)", allocated)
				}
			}
		})
	}
}

func TestHTMLCloneBudgetConservativeScanning(t *testing.T) {
	bomb := htmlReconstructionBomb(2000)
	for _, tt := range []struct{ name, input string }{
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
			if got, err := HTMLToTelegramText(tt.input); !errors.Is(err, ErrHTMLResourceLimit) || got != "" {
				t.Fatalf("got %d bytes, error %v; want resource limit error", len(got), err)
			}
		})
	}
	for _, name := range []string{"a", "b", "big", "code", "em", "font", "i", "nobr", "s", "small", "strike", "strong", "tt", "u"} {
		t.Run(name, func(t *testing.T) {
			input := strings.ReplaceAll(bomb, "<b ", "<"+name+" ")
			if _, err := HTMLToTelegramText(input); !errors.Is(err, ErrHTMLResourceLimit) {
				t.Fatalf("formatting tag %q escaped the clone budget: %v", name, err)
			}
		})
	}
}

func TestHTMLCloneBudgetAfterConditionalFiltering(t *testing.T) {
	var input strings.Builder
	input.WriteString("<p>")
	for i := 0; i < 700; i++ {
		fmt.Fprintf(&input, "<<![if false]>discard<![endif]>b id=%d>", i)
	}
	input.WriteString(strings.Repeat("<p>x", 700))
	if strings.Count(input.String(), "<") > maxHTMLMarkup {
		t.Fatal("regression must fit within the source-size limits")
	}
	filtered, err := filterHTMLConditionals(input.String())
	if err != nil || filtered != htmlReconstructionBomb(700) {
		t.Fatal("conditional deletion did not manufacture the expected tags")
	}
	if got, err := HTMLToTelegramText(input.String()); !errors.Is(err, ErrHTMLResourceLimit) || got != "" {
		t.Fatalf("got %d bytes, error %v; want resource limit error", len(got), err)
	}
	// Conversely, discarded branches do not contribute to DOM amplification.
	if got := mustHTMLText(t, "<![if false]>"+htmlReconstructionBomb(2000)+"<![endif]><p>Body</p>"); got != "Body" {
		t.Fatalf("got %q, want Body", got)
	}
}

func TestHTMLCloneBudgetEdges(t *testing.T) {
	// With only <b> starts and <p> tags, the budget is (2*markup+2)*128*count.
	const count = 100
	markup := maxHTMLCloneBytes/(2*128*count) - 1
	input := strings.Repeat("<b>", count) + strings.Repeat("<p>", markup-count)
	if err := checkHTMLCloneBudget(input); err != nil {
		t.Fatalf("at budget: %v", err)
	}
	if err := checkHTMLCloneBudget(input + "<p>"); !errors.Is(err, ErrHTMLResourceLimit) {
		t.Fatalf("over budget: %v", err)
	}
}

func TestHTMLCloneBudgetOrdinaryEmail(t *testing.T) {
	row := `<tr><td><b>Label</b></td><td><a href="https://example.com">Item</a></td></tr>`
	input := "<table>" + strings.Repeat(row, 100) + "</table>"
	wantRow := "Label | " + EncodeLinkToken("Item", "https://example.com")
	if got := mustHTMLText(t, input); got != strings.TrimSuffix(strings.Repeat(wantRow+"\n", 100), "\n") {
		t.Fatal("ordinary formatted table lost content")
	}
	// Longer non-formatting names must not be mistaken for formatting tags.
	for _, tag := range []string{"body", "base", "span", "image", "stronger"} {
		if name := htmlFormattingTagName(tag + ">"); name != "" {
			t.Fatalf("%s misidentified as formatting tag %s", tag, name)
		}
	}
}
