package formatting

import (
	"errors"
	"fmt"
	"runtime"
	"strings"
	"testing"
)

func TestHTMLParsingResourceLimits(t *testing.T) {
	for _, tt := range []struct{ name, input string }{
		{"deeply nested", strings.Repeat("<div>", 20000) + "Body" + strings.Repeat("</div>", 20000)},
		{"unclosed", strings.Repeat("<div>", maxHTMLMarkup+1)},
		{"misnested", strings.Repeat("<div></span>", maxHTMLMarkup)},
		{"foreign raw text", "<svg><style>" + strings.Repeat("<g>", maxHTMLMarkup)},
		{"conditional", "<![if false]>" + strings.Repeat("<div>", maxHTMLMarkup) + "<![endif]>"},
		{"oversized text", strings.Repeat("x", maxHTMLBytes+1)},
	} {
		t.Run(tt.name, func(t *testing.T) {
			for _, convert := range []func(string) (string, error){HTMLToTelegramText, StripHTML} {
				got, err := convert(tt.input)
				if !errors.Is(err, ErrHTMLResourceLimit) || got != "" {
					t.Fatalf("got %q, error %v; want no text and resource limit error", got, err)
				}
			}
		})
	}
}

func TestHTMLResourceLimitBeforeDOMAllocation(t *testing.T) {
	input := strings.Repeat("<div>", 20000) + "Body" + strings.Repeat("</div>", 20000)
	runtime.GC()
	var before, after runtime.MemStats
	runtime.ReadMemStats(&before)
	_, err := HTMLToTelegramText(input)
	runtime.ReadMemStats(&after)
	if !errors.Is(err, ErrHTMLResourceLimit) {
		t.Fatalf("expected resource limit error, got %v", err)
	}
	// A DOM for this input takes several MiB, even without rendering it.
	if allocated := after.TotalAlloc - before.TotalAlloc; allocated > 1<<20 {
		t.Fatalf("rejected HTML allocated %d bytes before failing", allocated)
	}
}

func TestHTMLParsingBudgetEdges(t *testing.T) {
	for _, input := range []string{
		strings.Repeat("x", maxHTMLBytes),
		strings.Repeat("<div>", maxHTMLMarkup) + "Body", // Unclosed tags still count.
	} {
		if _, err := HTMLToTelegramText(input); err != nil {
			t.Fatal(err)
		}
	}
}

func BenchmarkHTMLResourceLimit(b *testing.B) {
	for _, depth := range []int{20000, 40000} {
		input := strings.Repeat("<div>", depth) + "Body" + strings.Repeat("</div>", depth)
		b.Run(fmt.Sprint(depth), func(b *testing.B) {
			b.ReportAllocs()
			b.SetBytes(int64(len(input)))
			for i := 0; i < b.N; i++ {
				if _, err := HTMLToTelegramText(input); !errors.Is(err, ErrHTMLResourceLimit) {
					b.Fatal(err)
				}
			}
		})
	}
}

func BenchmarkHTMLMaximumNesting(b *testing.B) {
	input := strings.Repeat("<div>", maxHTMLMarkup) + "Body"
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if _, err := HTMLToTelegramText(input); err != nil {
			b.Fatal(err)
		}
	}
}
