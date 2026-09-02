package mailai

import (
	"bytes"
	"io"

	"github.com/ledongthuc/pdf"
)

// pdfToText extracts plain text from PDF bytes. It returns an empty string
// when the PDF has no extractable text layer (e.g. scanned documents).
func pdfToText(data []byte) string {
	r := bytes.NewReader(data)
	f, err := pdf.NewReader(r, int64(len(data)))
	if err != nil {
		return ""
	}
	b, err := f.GetPlainText()
	if err != nil {
		return ""
	}
	out, err := io.ReadAll(b)
	if err != nil {
		return ""
	}
	return string(out)
}
