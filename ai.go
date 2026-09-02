package mailai

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// chatMessage is an OpenAI-compatible chat message. Content is either a
// string (text) or a []chatPart (text + images for vision models).
type chatMessage struct {
	Role    string `json:"role"`
	Content any    `json:"content"`
}

// chatPart is a content block: text or an inline base64 image.
type chatPart struct {
	Type     string    `json:"type"`
	Text     string    `json:"text,omitempty"`
	ImageURL *imageURL `json:"image_url,omitempty"`
}

type imageURL struct {
	URL string `json:"url"`
}

type chatRequest struct {
	Model    string        `json:"model"`
	Messages []chatMessage `json:"messages"`
}

type chatResponse struct {
	Choices []struct {
		Message struct {
			Content string `json:"content"`
		} `json:"message"`
	} `json:"choices"`
}

// chat performs one chat-completion request against the OpenAI-compatible
// endpoint {BaseURL}/chat/completions.
func (c *Client) chat(ctx context.Context, model, system string, user any) (string, error) {
	endpoint := strings.TrimRight(c.ai.BaseURL, "/") + "/chat/completions"

	payload := chatRequest{
		Model: model,
		Messages: []chatMessage{
			{Role: "system", Content: system},
			{Role: "user", Content: user},
		},
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return "", fmt.Errorf("mailai: marshal request: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(body))
	if err != nil {
		return "", fmt.Errorf("mailai: build request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+c.ai.APIKey)

	hc := c.ai.HTTPClient
	if hc == nil {
		hc = &http.Client{Timeout: 3 * time.Minute}
	}
	resp, err := hc.Do(req)
	if err != nil {
		return "", fmt.Errorf("mailai: call %s: %w", endpoint, err)
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", fmt.Errorf("mailai: read response: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("mailai: %s: HTTP %d: %s", endpoint, resp.StatusCode, truncate(string(respBody), 500))
	}

	var cr chatResponse
	if err := json.Unmarshal(respBody, &cr); err != nil {
		return "", fmt.Errorf("mailai: bad JSON response: %w", err)
	}
	if len(cr.Choices) == 0 {
		return "", fmt.Errorf("mailai: no choices in response: %s", truncate(string(respBody), 300))
	}
	return cr.Choices[0].Message.Content, nil
}

// summarize builds the prompt from the emails (plus extracted PDF text and
// images) and calls the LLM. It automatically switches to the vision model
// when images are included, and falls back to a text-only request if the
// model rejects image input.
func (c *Client) summarize(ctx context.Context, emails []Email) (*Digest, error) {
	if len(emails) == 0 {
		return nil, fmt.Errorf("mailai: no emails to summarize")
	}

	var text strings.Builder
	var pdfText strings.Builder
	var images []chatPart
	pdfCount, imageCount, imagesSkipped := 0, 0, 0

	for i, m := range emails {
		fmt.Fprintf(&text, "\n=== Email %d ===\nFrom: %s\nDate: %s\nSubject: %s\nAttachments: %s\nBody:\n%s\n",
			i+1, m.From, formatDate(m.Date), m.Subject, attachNames(m.Attachments), truncate(m.Text, c.opts.MaxBodyChars))

		for _, a := range m.Attachments {
			switch {
			case a.MimeType == "application/pdf" && !c.opts.DisablePDFs:
				if t := pdfToText(a.Data); t != "" {
					fmt.Fprintf(&pdfText, "\n--- PDF %q (email %d) ---\n%s\n", a.Name, i+1, truncate(t, c.opts.MaxPDFChars))
					pdfCount++
				}
			case isSupportedImage(a.MimeType) && !c.opts.DisableImages:
				if c.ai.VisionModel != "" && len(images) < c.opts.MaxImages {
					images = append(images, chatPart{
						Type:     "image_url",
						ImageURL: &imageURL{URL: "data:" + a.MimeType + ";base64," + base64.StdEncoding.EncodeToString(a.Data)},
					})
					imageCount++
				} else {
					imagesSkipped++
				}
			}
		}
	}

	prompt := "Here are recent emails from the inbox:\n" + text.String()
	if pdfText.Len() > 0 {
		prompt += "\nText extracted from the attached PDFs:\n" + pdfText.String()
	}
	prompt += "\nAnalyze them and provide the summary."

	model := c.ai.Model
	var user any = prompt
	if len(images) > 0 {
		model = c.ai.VisionModel
		parts := []chatPart{{Type: "text", Text: prompt}}
		user = append(parts, images...)
	}

	reply, err := c.chat(ctx, model, c.systemPrompt(), user)
	if err != nil && len(images) > 0 && strings.Contains(strings.ToLower(err.Error()), "image") {
		// The model does not accept image input; retry text-only.
		reply, err = c.chat(ctx, c.ai.Model, c.systemPrompt(), prompt)
		if err == nil {
			imagesSkipped += imageCount
			imageCount = 0
			model = c.ai.Model
		}
	}
	if err != nil {
		return nil, err
	}

	return &Digest{
		Summary:       reply,
		Model:         model,
		Emails:        len(emails),
		PDFs:          pdfCount,
		Images:        imageCount,
		ImagesSkipped: imagesSkipped,
	}, nil
}

// isSupportedImage reports whether the media type is one commonly accepted
// by vision models (JPEG, PNG, GIF, WebP).
func isSupportedImage(mimeType string) bool {
	switch strings.ToLower(mimeType) {
	case "image/jpeg", "image/png", "image/gif", "image/webp":
		return true
	}
	return false
}

func formatDate(t time.Time) string {
	if t.IsZero() {
		return "(unknown)"
	}
	return t.Format("2006-01-02 15:04")
}
