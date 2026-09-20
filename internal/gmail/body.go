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

type bodySection struct {
	text string
	next *bodySection
}

// Candidates own their section lists. Mixed containers splice them in constant
// time; alternatives retain just one list. Only the final selection is joined.
type bodyCandidate struct {
	first, last *bodySection
	size        int // Bytes including separators.
	quality     bodyQuality
}

func newBodyCandidate(text string, quality bodyQuality) bodyCandidate {
	if text == "" {
		return bodyCandidate{}
	}
	section := &bodySection{text: text}
	return bodyCandidate{first: section, last: section, size: len(text), quality: quality}
}

func (body *bodyCandidate) append(candidate bodyCandidate) {
	if candidate.first == nil {
		return
	}
	if body.first == nil {
		*body = candidate
		return
	}
	body.last.next = candidate.first
	body.last = candidate.last
	body.size += len("\n\n") + candidate.size
	if candidate.quality > body.quality {
		body.quality = candidate.quality
	}
}

func (body bodyCandidate) join() string {
	if body.first == nil {
		return ""
	}
	if body.first == body.last {
		return body.first.text
	}
	var out strings.Builder
	out.Grow(body.size)
	for section := body.first; section != nil; section = section.next {
		if section != body.first {
			out.WriteString("\n\n")
		}
		out.WriteString(section.text)
	}
	return out.String()
}

// ExtractBodyAndAttachments chooses one representation per multipart/alternative
// group, retaining independent sections in multipart/mixed. Clean plain text is
// preferred; HTML is used when the plain alternative leaks email markup. A noisy
// plain alternative is still better than losing the message altogether.
func ExtractBodyAndAttachments(payload map[string]any) (string, []models.AttachmentMeta, error) {
	var attachments []models.AttachmentMeta
	var visit func(map[string]any, bool) (bodyCandidate, error)
	visit = func(part map[string]any, relatedRoot bool) (bodyCandidate, error) {
		mimeType, _ := anyString(part["mimeType"])
		mimeType = strings.ToLower(strings.TrimSpace(strings.SplitN(mimeType, ";", 2)[0]))
		filename, _ := anyString(part["filename"])
		body, _ := part["body"].(map[string]any)
		data, _ := anyString(body["data"])
		attachmentID, _ := anyString(body["attachmentId"])
		disposition, _, _ := mime.ParseMediaType(partHeader(part, "Content-Disposition"))
		isAttachment := attachmentID != "" || filename != "" || disposition == "attachment"
		// A related root may be the body despite its filename/disposition
		// (RFC 2387), but suppress its metadata only if we can represent it.
		// Attachment IDs still require a separate API fetch.
		if isAttachment && (!relatedRoot || attachmentID != "") {
			attachments = append(attachments, bodyAttachmentMeta(mimeType, filename, body))
			return bodyCandidate{}, nil
		}

		var selected bodyCandidate
		switch mimeType {
		case "text/plain":
			text := formatting.NormalizePlainText(decodeBodyData(data))
			quality := cleanPlainBody
			if formatting.HasEmailMarkupArtifacts(text) {
				quality = contaminatedPlainBody
			}
			selected = newBodyCandidate(text, quality)
		case "text/html":
			text, err := formatting.HTMLToTelegramText(decodeBodyData(data))
			if err != nil {
				return bodyCandidate{}, fmt.Errorf("convert HTML body: %w", err)
			}
			// Test readability without normalizing away meaningful <pre> spacing.
			if formatting.NormalizePlainText(text) != "" {
				selected = newBodyCandidate(text, htmlBody)
			}
		default:
			children, _ := part["parts"].([]any)
			root := -1
			if mimeType == "multipart/related" {
				root = relatedRootIndex(part, children)
			}
			for i, child := range children {
				childPart, _ := child.(map[string]any)
				// Visit even unselected alternatives/resources to retain metadata
				// and propagate conversion errors.
				candidate, err := visit(childPart, i == root)
				if err != nil {
					return bodyCandidate{}, fmt.Errorf("MIME part %d: %w", i, err)
				}
				switch mimeType {
				case "multipart/alternative":
					// Later equally suitable alternatives have higher sender fidelity.
					if candidate.first != nil && (selected.first == nil || candidate.quality <= selected.quality) {
						selected = candidate
					}
				case "multipart/related":
					// Related siblings are resources, not extra copies of the body.
					if i == root {
						selected = candidate
					}
				default:
					// Mixed (and unrecognized) containers have independent sections.
					selected.append(candidate)
				}
			}
		}
		if isAttachment && selected.first == nil {
			attachments = append(attachments, bodyAttachmentMeta(mimeType, filename, body))
		}
		return selected, nil
	}

	candidate, err := visit(payload, false)
	if err != nil {
		return "", nil, err
	}
	bodyText := candidate.join()
	const maxBodyTextRunes = 50000
	if len([]rune(bodyText)) > maxBodyTextRunes {
		bodyText = formatting.TruncateText(bodyText, maxBodyTextRunes) + "\n\u2026 (message truncated)"
	}
	return bodyText, attachments, nil
}

func bodyAttachmentMeta(mimeType, filename string, body map[string]any) models.AttachmentMeta {
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
	return models.AttachmentMeta{Filename: filename, MimeType: mimeType, Size: size}
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
