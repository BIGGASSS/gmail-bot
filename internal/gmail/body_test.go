package gmail

import (
	"errors"
	"os"
	"runtime"
	"strings"
	"testing"

	"github.com/BIGGASSS/gmail-bot/internal/formatting"
)

func textPart(mimeType, text string) map[string]any {
	return map[string]any{"mimeType": mimeType, "body": map[string]any{"data": encodeBody(text)}}
}

func multipart(mimeType string, children ...map[string]any) map[string]any {
	parts := make([]any, len(children))
	for i, child := range children {
		parts[i] = child
	}
	return map[string]any{"mimeType": mimeType, "parts": parts}
}

func withPartHeaders(part map[string]any, headers ...string) map[string]any {
	var values []any
	for i := 0; i < len(headers); i += 2 {
		values = append(values, map[string]any{"name": headers[i], "value": headers[i+1]})
	}
	part["headers"] = values
	return part
}

func TestAlternativeBodySelection(t *testing.T) {
	noisy := "Notice\n\n<!--[if !mso]><!-->\n<v:roundrect>"
	for _, tt := range []struct {
		name    string
		payload map[string]any
		want    string
	}{
		{"clean plain preferred", multipart("multipart/alternative", textPart("text/html", "<p>HTML</p>"), textPart("text/plain", "Plain")), "Plain"},
		{"contaminated plain", multipart("multipart/alternative", textPart("text/plain", noisy), textPart("text/html", "<p>Readable</p>")), "Readable"},
		{"empty plain", multipart("multipart/alternative", textPart("text/plain", "\n \u200a\u200b\n"), textPart("text/html", "<p>Readable</p>")), "Readable"},
		{"no HTML alternative", textPart("text/plain", noisy), noisy},
		{"empty HTML fallback", multipart("multipart/alternative", textPart("text/plain", noisy), textPart("text/html", "<style>style</style><div hidden>hidden</div>")), noisy},
		{"whitespace HTML fallback", multipart("multipart/alternative", textPart("text/plain", noisy), textPart("text/html", "<pre> \t\n </pre>")), noisy},
		{"invisible HTML fallback", multipart("multipart/alternative", textPart("text/plain", noisy), textPart("text/html", "<pre>\u200b</pre>")), noisy},
		{"invisible HTML before plain", multipart("multipart/alternative", textPart("text/html", "<pre>\u200b</pre>"), textPart("text/plain", noisy)), noisy},
		{"all invisible padding fallback", multipart("multipart/alternative", textPart("text/plain", noisy), textPart("text/html", "<pre> \t\n\u200a\u200b\u200c\u200d\ufeff\u00ad\u00a0</pre>")), noisy},
		{"nested invisible HTML fallback", multipart("multipart/alternative", textPart("text/plain", noisy), multipart("multipart/related", textPart("text/html", "<pre>\u200b</pre>"))), noisy},
		{"readable pre preserves whitespace", multipart("multipart/alternative", textPart("text/plain", noisy), textPart("text/html", "<pre>  first\n\n\n\tsecond  \n </pre>")), "  first\n\n\n\tsecond  \n "},
		{"readable pre preserves invisible characters", multipart("multipart/alternative", textPart("text/plain", noisy), textPart("text/html", "<pre>  join\u200der\n\tend</pre>")), "  join\u200der\n\tend"},
		{"invisible standalone HTML", textPart("text/html", "<pre>\u200b</pre>"), ""},
		{"bad base64 fallback", multipart("multipart/alternative", textPart("text/plain", noisy), map[string]any{"mimeType": "text/html", "body": map[string]any{"data": "!bad base64!"}}), noisy},
		{"one alternative only", multipart("multipart/alternative", textPart("text/plain", "First"), textPart("text/plain", "Last")), "Last"},
		{"independent mixed sections", multipart("multipart/mixed", textPart("text/plain", "Intro"), textPart("text/html", "<p>HTML section</p>")), "Intro\n\nHTML section"},
		{"nested mixed section order", multipart("multipart/mixed", textPart("text/plain", "First"), multipart("multipart/mixed", textPart("text/plain", "Second"), textPart("text/plain", ""), textPart("text/plain", "Third")), textPart("text/plain", "Last")), "First\n\nSecond\n\nThird\n\nLast"},
		{"mixed alternative retains all sections", multipart("multipart/alternative", textPart("text/plain", noisy), multipart("multipart/mixed", textPart("text/plain", "First"), textPart("text/html", "<p>Second</p>"))), "First\n\nSecond"},
		{"mixed quality includes every section", multipart("multipart/alternative", multipart("multipart/mixed", textPart("text/plain", noisy), textPart("text/plain", "Clean")), textPart("text/html", "<p>Readable</p>")), "Readable"},
		{"groups select independently", multipart("multipart/mixed",
			multipart("multipart/alternative", textPart("text/plain", "Clean"), textPart("text/html", "<p>Duplicate clean</p>")),
			multipart("multipart/alternative", textPart("text/plain", noisy), textPart("text/html", "<p>Readable</p>"))), "Clean\n\nReadable"},
		{"nested related alternative", multipart("multipart/alternative", textPart("text/plain", noisy),
			multipart("multipart/related", textPart("text/html", "<p>Readable</p>"), textPart("text/plain", "Resource not a body"))), "Readable"},
		{"nested alternative within related", multipart("multipart/related", multipart("multipart/alternative", textPart("text/plain", "Clean"), textPart("text/html", "<p>HTML</p>")), textPart("text/plain", "Resource")), "Clean"},
		{"no global deduplication", multipart("multipart/mixed", textPart("text/plain", "Repeat"), textPart("text/html", "<p>Repeat</p>")), "Repeat\n\nRepeat"},
		{"ordinary literal markup", multipart("multipart/alternative", textPart("text/plain", "Contact <person@example.com> &amp; use <div>."), textPart("text/html", "<p>Other</p>")), "Contact <person@example.com> &amp; use <div>."},
		{"case and MIME parameters", multipart("MULTIPART/ALTERNATIVE", textPart("Text/Plain; charset=utf-8", "Plain"), textPart("TEXT/HTML", "<p>HTML</p>")), "Plain"},
		{"plain indentation", textPart("text/plain", "\n  first\n\tsecond\n\n\nlast\n"), "  first\n\tsecond\n\nlast"},
		{"HTML pre indentation", textPart("text/html", "<pre>  first\n\tsecond</pre>"), "  first\n\tsecond"},
		{"nil payload", nil, ""},
		{"empty multipart", multipart("multipart/related"), ""},
		{"invalid child", map[string]any{"mimeType": "multipart/mixed", "parts": []any{nil, "invalid", textPart("text/plain", "Body")}}, "Body"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			got, _, err := ExtractBodyAndAttachments(tt.payload)
			if err != nil {
				t.Fatal(err)
			}
			if got != tt.want {
				t.Fatalf("got %q, want %q", got, tt.want)
			}
		})
	}
}

func TestRelatedRootSelection(t *testing.T) {
	for _, tt := range []struct{ name, contentType, want string }{
		{"default first", "multipart/related", "Resource"},
		{"explicit root", `multipart/related; start="<body@example.invalid>"`, "Body"},
		{"missing root fallback", `multipart/related; start="<missing>"`, "Resource"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			payload := withPartHeaders(multipart("multipart/related", textPart("text/plain", "Resource"),
				withPartHeaders(textPart("text/html", "<p>Body</p>"), "content-id", "<body@example.invalid>")), "content-type", tt.contentType)
			got, _, err := ExtractBodyAndAttachments(payload)
			if err != nil {
				t.Fatal(err)
			}
			if got != tt.want {
				t.Fatalf("got %q, want %q", got, tt.want)
			}
		})
	}
}

func TestRelatedRootIgnoresAttachmentDisposition(t *testing.T) {
	for _, root := range []map[string]any{
		textPart("text/html", "<pre>  Root body</pre>"),
		textPart("text/plain", "  Root body"),
		multipart("multipart/alternative", textPart("text/plain", "  Root body"), textPart("text/html", "<p>Other representation</p>")),
	} {
		for _, named := range []bool{false, true} {
			if named {
				root["filename"] = "body"
			}
			for _, disposition := range []string{"", "inline", "attachment"} {
				withPartHeaders(root, "Content-ID", "<body@example.invalid>", "Content-Disposition", disposition)
				for _, explicit := range []bool{false, true} {
					payload := multipart("multipart/related", root, textPart("text/plain", "Resource"))
					if explicit {
						payload = withPartHeaders(multipart("multipart/related", textPart("text/plain", "Resource"), root),
							"Content-Type", `multipart/related; start="<body@example.invalid>"`)
					}
					body, attachments, err := ExtractBodyAndAttachments(payload)
					if err != nil {
						t.Fatal(err)
					}
					if body != "  Root body" || len(attachments) != 0 {
						t.Fatalf("MIME=%v named=%v disposition=%q explicit=%v: got body %q, attachments %+v", root["mimeType"], named, disposition, explicit, body, attachments)
					}
				}
			}
		}
	}
}

func TestRelatedRootPreservesUnrepresentedAttachments(t *testing.T) {
	for _, tt := range []struct {
		name, mimeType, content, filename, disposition, attachmentID string
	}{
		{"named SVG", "image/svg+xml", "<svg></svg>", "report.svg", "inline", ""},
		{"named image", "image/png", "inline image data", "image.png", "inline", ""},
		{"PDF disposition", "application/pdf", "inline PDF data", "", "attachment", ""},
		{"calendar filename and disposition", "text/calendar", "inline calendar data", "invite.ics", "attachment", ""},
		{"unknown MIME", "", "inline data", "unknown", "", ""},
		{"empty plain", "text/plain", "", "empty.txt", "", ""},
		{"hidden HTML", "text/html", "<div hidden>hidden</div>", "empty.html", "", ""},
		{"invisible HTML", "text/html", "<pre>\u200b</pre>", "", "attachment", ""},
		{"empty multipart", "multipart/mixed", "", "empty.mime", "attachment", ""},
		{"attachment ID only", "text/html", "", "", "", "body-id"},
		{"attachment ID with data", "text/html", "<p>Body</p>", "body.html", "", "body-id"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			root := withPartHeaders(textPart(tt.mimeType, tt.content), "Content-ID", "<body@example.invalid>", "Content-Disposition", tt.disposition)
			root["filename"] = tt.filename
			root["body"].(map[string]any)["size"] = "42"
			root["body"].(map[string]any)["attachmentId"] = tt.attachmentID
			for _, explicit := range []bool{false, true} {
				payload := multipart("multipart/related", root, textPart("text/plain", "Resource, not a fallback body"))
				if explicit {
					payload = withPartHeaders(multipart("multipart/related", textPart("text/plain", "Resource, not a fallback body"), root),
						"Content-Type", `multipart/related; start="<body@example.invalid>"`)
				}
				body, attachments, err := ExtractBodyAndAttachments(payload)
				if err != nil {
					t.Fatal(err)
				}
				if body != "" || len(attachments) != 1 {
					t.Fatalf("explicit=%v: got body %q, attachments %+v", explicit, body, attachments)
				}
				wantFilename, wantMIME := tt.filename, tt.mimeType
				if wantFilename == "" {
					wantFilename = "(unnamed attachment)"
				}
				if wantMIME == "" {
					wantMIME = "application/octet-stream"
				}
				if attachments[0].Filename != wantFilename || attachments[0].MimeType != wantMIME || attachments[0].Size != 42 {
					t.Fatalf("explicit=%v: unexpected attachment metadata: %+v", explicit, attachments)
				}
			}
		})
	}
}

func TestBodySelectionPreservesAttachments(t *testing.T) {
	attachment := textPart("text/plain", "Attachment contents are not the body")
	attachment["filename"] = "notes.txt"
	attachment["body"].(map[string]any)["size"] = 128
	inline := map[string]any{"mimeType": "image/png", "body": map[string]any{"attachmentId": "image-1", "size": 42}}
	unnamed := withPartHeaders(textPart("text/plain", "Also an attachment"), "Content-Disposition", "attachment")
	payload := multipart("multipart/mixed",
		multipart("multipart/alternative", textPart("text/plain", "Clean"),
			multipart("multipart/related", textPart("text/html", "<p>Unselected HTML</p>"), inline)), attachment, unnamed)
	body, attachments, err := ExtractBodyAndAttachments(payload)
	if err != nil {
		t.Fatal(err)
	}
	if body != "Clean" {
		t.Fatalf("got body %q", body)
	}
	if len(attachments) != 3 || attachments[0].MimeType != "image/png" || attachments[0].Size != 42 || attachments[1].Filename != "notes.txt" || attachments[1].Size != 128 || attachments[2].Filename != "(unnamed attachment)" {
		t.Fatalf("unexpected attachment metadata: %+v", attachments)
	}
}

func TestExtractBodyAndAttachmentsRejectsHTMLResourceLimit(t *testing.T) {
	html := textPart("text/html", strings.Repeat("<div>", 20000)+"Deep body"+strings.Repeat("</div>", 20000))
	for _, tt := range []struct {
		name string
		part map[string]any
	}{
		{"HTML only", html},
		{"plain before HTML", multipart("multipart/alternative", textPart("text/plain", "Clean plain body"), html)},
		{"plain after HTML", multipart("multipart/alternative", html, textPart("text/plain", "Clean plain body"))},
	} {
		t.Run(tt.name, func(t *testing.T) {
			attachment := textPart("application/pdf", "PDF data")
			attachment["filename"] = "report.pdf"
			payload := multipart("multipart/mixed", textPart("text/plain", "Partial body"), attachment, tt.part)
			body, attachments, err := ExtractBodyAndAttachments(payload)
			if !errors.Is(err, formatting.ErrHTMLResourceLimit) {
				t.Fatalf("got error %v, want ErrHTMLResourceLimit", err)
			}
			if body != "" || len(attachments) != 0 {
				t.Fatalf("error returned partial results: body %q, attachments %+v", body, attachments)
			}
		})
	}
}

func nestedMixedBody(depth int) map[string]any {
	payload := textPart("text/plain", "End")
	section := strings.Repeat("x", 1024)
	for i := 0; i < depth; i++ {
		payload = multipart("multipart/mixed", textPart("text/plain", section), payload)
	}
	return payload
}

func TestExtractBodyAndAttachmentsNestedMixedAllocations(t *testing.T) {
	// Each level has a nonempty 1 KiB sibling. Joining at every ancestor used
	// about 135 MiB for this half-megabyte message, even though output is capped.
	payload := nestedMixedBody(512)
	want := strings.Repeat(strings.Repeat("x", 1024)+"\n\n", 512)[:50000] + "\n… (message truncated)"
	var before, after runtime.MemStats
	runtime.GC()
	runtime.ReadMemStats(&before)
	body, attachments, err := ExtractBodyAndAttachments(payload)
	runtime.ReadMemStats(&after)
	if err != nil {
		t.Fatal(err)
	}
	if body != want || len(attachments) != 0 {
		t.Fatal("nested mixed message lost section boundaries or truncation")
	}
	// Allow headroom for decoding, normalization, final joining and truncation,
	// but not repeated full-body copies as the selection travels up the tree.
	if allocated := after.TotalAlloc - before.TotalAlloc; allocated > 12<<20 {
		t.Fatalf("nested mixed message allocated %d bytes, want <= 12 MiB", allocated)
	}
}

func BenchmarkExtractBodyAndAttachmentsNestedMixed(b *testing.B) {
	payload := nestedMixedBody(512)
	b.ReportAllocs()
	b.SetBytes(512 * 1024)
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_, _, err := ExtractBodyAndAttachments(payload)
		if err != nil {
			b.Fatal(err)
		}
	}
}

func TestAccountNoticeMIMEFixture(t *testing.T) {
	// Synthetic fixtures model the reported failure; they contain no user data.
	plain, err := os.ReadFile("testdata/account-notice-plain.txt")
	if err != nil {
		t.Fatal(err)
	}
	html, err := os.ReadFile("../formatting/testdata/account-notice.html")
	if err != nil {
		t.Fatal(err)
	}
	want, err := os.ReadFile("../formatting/testdata/account-notice.want.txt")
	if err != nil {
		t.Fatal(err)
	}
	body, _, err := ExtractBodyAndAttachments(multipart("multipart/alternative", textPart("text/plain", string(plain)), textPart("text/html", string(html))))
	if err != nil {
		t.Fatal(err)
	}
	if got := formatting.RenderTelegramHTML(body); got != strings.TrimSuffix(string(want), "\n") {
		t.Fatalf("unexpected rendered account notice:\n%s\nwant:\n%s", got, want)
	}
	for _, chunk := range formatting.RenderAndChunk(body, formatting.SafeByteLimit) {
		if len(chunk) > formatting.SafeByteLimit || strings.ContainsAny(chunk, "\ufff0\ufff1\ufff2") {
			t.Fatal("invalid Telegram chunk")
		}
	}
}
