package gmail

import (
	"os"
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
		{"bad base64 fallback", multipart("multipart/alternative", textPart("text/plain", noisy), map[string]any{"mimeType": "text/html", "body": map[string]any{"data": "!bad base64!"}}), noisy},
		{"one alternative only", multipart("multipart/alternative", textPart("text/plain", "First"), textPart("text/plain", "Last")), "Last"},
		{"independent mixed sections", multipart("multipart/mixed", textPart("text/plain", "Intro"), textPart("text/html", "<p>HTML section</p>")), "Intro\n\nHTML section"},
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
			got, _ := ExtractBodyAndAttachments(tt.payload)
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
			got, _ := ExtractBodyAndAttachments(payload)
			if got != tt.want {
				t.Fatalf("got %q, want %q", got, tt.want)
			}
		})
	}
}

func TestRelatedRootIgnoresAttachmentDisposition(t *testing.T) {
	for _, named := range []bool{false, true} {
		root := withPartHeaders(textPart("text/html", "<pre>  Root body</pre>"),
			"Content-ID", "<body@example.invalid>", "Content-Disposition", "attachment")
		if named {
			root["filename"] = "body.html"
		}
		for _, explicit := range []bool{false, true} {
			payload := multipart("multipart/related", root, textPart("text/plain", "Resource"))
			if explicit {
				payload = withPartHeaders(multipart("multipart/related", textPart("text/plain", "Resource"), root),
					"Content-Type", `multipart/related; start="<body@example.invalid>"`)
			}
			body, attachments := ExtractBodyAndAttachments(payload)
			if body != "  Root body" || len(attachments) != 0 {
				t.Fatalf("named=%v explicit=%v: got body %q, attachments %+v", named, explicit, body, attachments)
			}
		}
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
	body, attachments := ExtractBodyAndAttachments(payload)
	if body != "Clean" {
		t.Fatalf("got body %q", body)
	}
	if len(attachments) != 3 || attachments[0].MimeType != "image/png" || attachments[0].Size != 42 || attachments[1].Filename != "notes.txt" || attachments[1].Size != 128 || attachments[2].Filename != "(unnamed attachment)" {
		t.Fatalf("unexpected attachment metadata: %+v", attachments)
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
	body, _ := ExtractBodyAndAttachments(multipart("multipart/alternative", textPart("text/plain", string(plain)), textPart("text/html", string(html))))
	if got := formatting.RenderTelegramHTML(body); got != strings.TrimSuffix(string(want), "\n") {
		t.Fatalf("unexpected rendered account notice:\n%s\nwant:\n%s", got, want)
	}
	for _, chunk := range formatting.RenderAndChunk(body, formatting.SafeByteLimit) {
		if len(chunk) > formatting.SafeByteLimit || strings.ContainsAny(chunk, "\ufff0\ufff1\ufff2") {
			t.Fatal("invalid Telegram chunk")
		}
	}
}
