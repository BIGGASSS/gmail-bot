package gmail

import (
	"fmt"
	"mime"
	"strings"

	"github.com/BIGGASSS/gmail-bot/internal/formatting"
	"github.com/BIGGASSS/gmail-bot/internal/models"
)

type bodyQuality int

const (
	cleanPlainBody bodyQuality = iota + 1
	htmlBody
	contaminatedPlainBody
)

type bodyCandidate struct {
	text    string
	quality bodyQuality
}

// ExtractBodyAndAttachments chooses one representation per multipart/alternative
// group, retaining independent sections in multipart/mixed. Clean plain text is
// preferred; HTML is used when the plain alternative leaks email markup. A noisy
// plain alternative is still better than losing the message altogether.
func ExtractBodyAndAttachments(payload map[string]any) (string, []models.AttachmentMeta) {
	var attachments []models.AttachmentMeta
	var visit func(map[string]any, bool) bodyCandidate
	visit = func(part map[string]any, relatedRoot bool) bodyCandidate {
		mimeType, _ := anyString(part["mimeType"])
		mimeType = strings.ToLower(strings.TrimSpace(strings.SplitN(mimeType, ";", 2)[0]))
		filename, _ := anyString(part["filename"])
		body, _ := part["body"].(map[string]any)
		data, _ := anyString(body["data"])
		attachmentID, _ := anyString(body["attachmentId"])
		disposition, _, _ := mime.ParseMediaType(partHeader(part, "Content-Disposition"))
		// A related root is the body even when it has a filename or attachment
		// disposition (RFC 2387). Attachment-ID-only bodies still require a
		// separate API fetch, which this payload-only extractor cannot perform.
		if attachmentID != "" || (!relatedRoot && (filename != "" || disposition == "attachment")) {
			size := 0
			switch v := body["size"].(type) {
			case float64:
				size = int(v)
			case string:
				fmt.Sscan(v, &size)
			case int:
				size = v
			}
			if filename == "" {
				filename = "(unnamed attachment)"
			}
			if mimeType == "" {
				mimeType = "application/octet-stream"
			}
			attachments = append(attachments, models.AttachmentMeta{Filename: filename, MimeType: mimeType, Size: size})
			return bodyCandidate{}
		}

		switch mimeType {
		case "text/plain":
			text := formatting.NormalizePlainText(decodeBodyData(data))
			quality := cleanPlainBody
			if formatting.HasEmailMarkupArtifacts(text) {
				quality = contaminatedPlainBody
			}
			return bodyCandidate{text: text, quality: quality}
		case "text/html":
			text := formatting.HTMLToTelegramText(decodeBodyData(data))
			if strings.TrimSpace(text) == "" {
				text = "" // An empty <pre> must not displace a readable alternative.
			}
			return bodyCandidate{text: text, quality: htmlBody}
		}

		children, _ := part["parts"].([]any)
		root := -1
		if mimeType == "multipart/related" {
			root = relatedRootIndex(part, children)
		}
		candidates := make([]bodyCandidate, 0, len(children))
		for i, child := range children {
			childPart, _ := child.(map[string]any)
			// Visit even unselected alternatives/resources to retain metadata.
			candidates = append(candidates, visit(childPart, i == root))
		}
		switch mimeType {
		case "multipart/alternative":
			var best bodyCandidate
			for _, candidate := range candidates {
				// Later equally suitable alternatives have higher sender fidelity.
				if candidate.text != "" && (best.text == "" || candidate.quality <= best.quality) {
					best = candidate
				}
			}
			return best
		case "multipart/related":
			if len(candidates) == 0 {
				return bodyCandidate{}
			}
			// Related siblings are resources, not extra copies of the body.
			return candidates[root]
		default:
			// Mixed (and unrecognized) containers contain independent sections.
			var pieces []string
			var quality bodyQuality
			for _, candidate := range candidates {
				if candidate.text != "" {
					pieces = append(pieces, candidate.text)
					if candidate.quality > quality {
						quality = candidate.quality
					}
				}
			}
			return bodyCandidate{text: strings.Join(pieces, "\n\n"), quality: quality}
		}
	}

	bodyText := visit(payload, false).text
	const maxBodyTextRunes = 50000
	if len([]rune(bodyText)) > maxBodyTextRunes {
		bodyText = formatting.TruncateText(bodyText, maxBodyTextRunes) + "\n\u2026 (message truncated)"
	}
	return bodyText, attachments
}

// RFC 2387: start identifies the related root; otherwise use the first part.
func relatedRootIndex(part map[string]any, children []any) int {
	_, params, _ := mime.ParseMediaType(partHeader(part, "Content-Type"))
	if start := strings.Trim(params["start"], "<>"); start != "" {
		for i, child := range children {
			childPart, _ := child.(map[string]any)
			if strings.Trim(partHeader(childPart, "Content-ID"), "<>") == start {
				return i
			}
		}
	}
	return 0
}

func partHeader(part map[string]any, name string) string {
	headers, _ := part["headers"].([]any)
	for _, item := range headers {
		header, _ := item.(map[string]any)
		key, _ := anyString(header["name"])
		if strings.EqualFold(key, name) {
			value, _ := anyString(header["value"])
			return strings.TrimSpace(value)
		}
	}
	return ""
}
