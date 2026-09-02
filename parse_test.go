package mailai

import (
	"bytes"
	"encoding/base64"
	"mime/multipart"
	"net/textproto"
	"strings"
	"testing"
)

// buildRawMessage constructs a multipart email with:
//   - multipart/alternative containing text/plain and text/html
//   - a base64-encoded PDF attachment
//   - an inline image (no filename) that must be treated as an attachment
func buildRawMessage(t *testing.T) []byte {
	t.Helper()

	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	mw.SetBoundary("OUTER")

	// ---- multipart/alternative part ----
	altHdr := textproto.MIMEHeader{}
	altHdr.Set("Content-Type", `multipart/alternative; boundary="INNER"`)
	alt, err := mw.CreatePart(altHdr)
	if err != nil {
		t.Fatal(err)
	}
	aw := multipart.NewWriter(alt)
	aw.SetBoundary("INNER")

	plainHdr := textproto.MIMEHeader{}
	plainHdr.Set("Content-Type", "text/plain; charset=utf-8")
	plainHdr.Set("Content-Transfer-Encoding", "quoted-printable")
	plain, err := aw.CreatePart(plainHdr)
	if err != nil {
		t.Fatal(err)
	}
	// "café" in UTF-8, quoted-printable encoded.
	plain.Write([]byte("Hola mundo caf=C3=A9"))

	htmlHdr := textproto.MIMEHeader{}
	htmlHdr.Set("Content-Type", "text/html; charset=utf-8")
	html, err := aw.CreatePart(htmlHdr)
	if err != nil {
		t.Fatal(err)
	}
	html.Write([]byte("<html><body><p>Hola <b>mundo</b></p><script>alert(1)</script></body></html>"))
	aw.Close()

	// ---- PDF attachment ----
	pdfHdr := textproto.MIMEHeader{}
	pdfHdr.Set("Content-Type", "application/pdf")
	pdfHdr.Set("Content-Disposition", `attachment; filename="=?utf-8?q?informe=5F2026.pdf?="`)
	pdfHdr.Set("Content-Transfer-Encoding", "base64")
	pdf, err := mw.CreatePart(pdfHdr)
	if err != nil {
		t.Fatal(err)
	}
	pdf.Write([]byte(base64.StdEncoding.EncodeToString([]byte("PDFDATA"))))

	// ---- inline image without filename ----
	imgHdr := textproto.MIMEHeader{}
	imgHdr.Set("Content-Type", "image/png")
	imgHdr.Set("Content-Disposition", "inline")
	imgHdr.Set("Content-Transfer-Encoding", "base64")
	img, err := mw.CreatePart(imgHdr)
	if err != nil {
		t.Fatal(err)
	}
	img.Write([]byte(base64.StdEncoding.EncodeToString([]byte("PNGDATA"))))

	mw.Close()

	// Prepend minimal RFC 5322 headers so mail.ReadMessage can parse it.
	head := "From: a@example.com\r\nTo: b@example.com\r\nSubject: test\r\n" +
		"MIME-Version: 1.0\r\nContent-Type: multipart/mixed; boundary=\"OUTER\"\r\n\r\n"
	return append([]byte(head), buf.Bytes()...)
}

func TestParseMessageTextAndAttachments(t *testing.T) {
	text, atts := parseMessage(buildRawMessage(t))

	// text/plain is preferred over text/html.
	if !strings.Contains(text, "Hola mundo café") {
		t.Fatalf("expected decoded plain text, got: %q", text)
	}
	if strings.Contains(text, "<b>") || strings.Contains(text, "alert") {
		t.Fatalf("HTML leaked into text: %q", text)
	}

	if len(atts) != 2 {
		t.Fatalf("expected 2 attachments, got %d: %+v", len(atts), atts)
	}

	pdf := atts[0]
	if pdf.MimeType != "application/pdf" {
		t.Errorf("pdf mime = %q, want application/pdf", pdf.MimeType)
	}
	if pdf.Name != "informe_2026.pdf" {
		t.Errorf("pdf name = %q, want RFC 2047 decoded name", pdf.Name)
	}
	if string(pdf.Data) != "PDFDATA" {
		t.Errorf("pdf data = %q, want PDFDATA", pdf.Data)
	}

	img := atts[1]
	if img.MimeType != "image/png" {
		t.Errorf("img mime = %q, want image/png", img.MimeType)
	}
	if string(img.Data) != "PNGDATA" {
		t.Errorf("img data = %q, want PNGDATA", img.Data)
	}
}

func TestParseMessagePrefersPlainText(t *testing.T) {
	raw := "From: a@example.com\r\nTo: b@example.com\r\nSubject: x\r\n" +
		"MIME-Version: 1.0\r\nContent-Type: multipart/alternative; boundary=\"B\"\r\n\r\n" +
		"--B\r\nContent-Type: text/html\r\n\r\n<p>HTML body</p>\r\n" +
		"--B\r\nContent-Type: text/plain\r\n\r\nPlain body\r\n" +
		"--B--\r\n"

	text, _ := parseMessage([]byte(raw))
	if strings.TrimSpace(text) != "Plain body" {
		t.Fatalf("expected plain alternative to win, got: %q", text)
	}
}

func TestHTMLToText(t *testing.T) {
	in := `<html><style>p{color:red}</style><body><p>Hello&nbsp;world &amp; more</p><script>evil()</script></body></html>`
	out := htmlToText(in)
	if out != "Hello world & more" {
		t.Fatalf("htmlToText = %q", out)
	}
}
