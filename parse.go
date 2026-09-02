package mailai

import (
	"bytes"
	"encoding/base64"
	"io"
	"mime"
	"mime/multipart"
	"mime/quotedprintable"
	"net/mail"
	"net/textproto"
	"regexp"
	"strings"
)

// parseMessage splits a raw RFC 5322 message into readable text and decoded
// attachments. It prefers the text/plain alternative over text/html.
func parseMessage(raw []byte) (string, []Attachment) {
	msg, err := mail.ReadMessage(bytes.NewReader(raw))
	if err != nil {
		return "", nil
	}
	plain, html, atts, err := walkPart(textproto.MIMEHeader(msg.Header), msg.Body)
	if err != nil {
		// Best effort: return whatever was collected.
	}
	text := plain
	if text == "" {
		text = html
	}
	return text, atts
}

// walkPart recursively walks a MIME tree, returning the plain and HTML text
// alternatives and all attachments.
func walkPart(h textproto.MIMEHeader, body io.Reader) (plain, html string, atts []Attachment, err error) {
	mediatype, params, err := mime.ParseMediaType(h.Get("Content-Type"))
	if err != nil {
		mediatype = "text/plain"
	}

	if strings.HasPrefix(mediatype, "multipart/") {
		mr := multipart.NewReader(body, params["boundary"])
		for {
			p, err := mr.NextPart()
			if err == io.EOF {
				break
			}
			if err != nil {
				return plain, html, atts, err
			}
			pPlain, pHtml, pAtts, err := walkPart(p.Header, p)
			if err != nil {
				continue
			}
			plain += pPlain
			html += pHtml
			atts = append(atts, pAtts...)
		}
		return plain, html, atts, nil
	}

	data, err := io.ReadAll(body)
	if err != nil {
		return "", "", nil, err
	}

	cd := h.Get("Content-Disposition")
	_, cdParams, _ := mime.ParseMediaType(cd)
	name := cdParams["filename"]
	isAttachment := name != "" ||
		strings.HasPrefix(strings.ToLower(cd), "attachment") ||
		mediatype == "application/pdf" ||
		strings.HasPrefix(mediatype, "image/")

	if isAttachment {
		decoded := decodeCTE(h.Get("Content-Transfer-Encoding"), data)
		return "", "", []Attachment{{Name: decodeHeader(name), MimeType: mediatype, Data: decoded}}, nil
	}

	decoded := decodeCTE(h.Get("Content-Transfer-Encoding"), data)
	switch mediatype {
	case "text/plain":
		return decodeHeader(string(decoded)), "", nil, nil
	case "text/html":
		return "", htmlToText(string(decoded)), nil, nil
	}
	return "", "", nil, nil
}

// decodeCTE decodes base64 and quoted-printable transfer encodings.
func decodeCTE(enc string, data []byte) []byte {
	switch strings.ToLower(enc) {
	case "base64":
		if out, err := base64.StdEncoding.DecodeString(strings.TrimSpace(string(data))); err == nil {
			return out
		}
	case "quoted-printable":
		r := quotedprintable.NewReader(bytes.NewReader(data))
		if out, err := io.ReadAll(r); err == nil {
			return out
		}
	}
	return data
}

var tagRe = regexp.MustCompile(`(?s)<script.*?</script>|<style.*?</style>|<[^>]+>`)

// htmlToText strips HTML tags and converts common entities to plain text.
func htmlToText(s string) string {
	s = tagRe.ReplaceAllString(s, " ")
	s = strings.ReplaceAll(s, "&nbsp;", " ")
	s = strings.ReplaceAll(s, "&amp;", "&")
	s = strings.ReplaceAll(s, "&lt;", "<")
	s = strings.ReplaceAll(s, "&gt;", ">")
	s = strings.ReplaceAll(s, "&quot;", `"`)
	s = strings.ReplaceAll(s, "&#39;", "'")
	return strings.Join(strings.Fields(s), " ")
}

// decodeHeader decodes RFC 2047 encoded words (e.g. =?utf-8?q?...?=).
func decodeHeader(s string) string {
	if s == "" {
		return ""
	}
	dec := new(mime.WordDecoder)
	if out, err := dec.DecodeHeader(s); err == nil {
		return out
	}
	return s
}

func truncate(s string, n int) string {
	if n <= 0 || len(s) <= n {
		return s
	}
	return s[:n] + "..."
}

func attachNames(atts []Attachment) string {
	if len(atts) == 0 {
		return "(none)"
	}
	names := make([]string, 0, len(atts))
	for _, a := range atts {
		n := a.Name
		if n == "" {
			n = a.MimeType
		}
		names = append(names, n)
	}
	return strings.Join(names, ", ")
}
