package mailai

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// decodeRequest returns the model and the user content of a captured request.
func decodeRequest(t *testing.T, raw []byte) (string, any) {
	t.Helper()
	var req chatRequest
	if err := json.Unmarshal(raw, &req); err != nil {
		t.Fatalf("bad request JSON: %v", err)
	}
	return req.Model, req.Messages[len(req.Messages)-1].Content
}

func TestSummarizeUsesVisionModelForImages(t *testing.T) {
	var gotModel string
	var gotContent any

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/chat/completions" {
			t.Errorf("path = %q, want /chat/completions", r.URL.Path)
		}
		if r.Header.Get("Authorization") != "Bearer test-key" {
			t.Errorf("auth = %q, want Bearer test-key", r.Header.Get("Authorization"))
		}
		var body json.RawMessage
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Errorf("decode: %v", err)
		}
		gotModel, gotContent = decodeRequest(t, body)
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"choices":[{"message":{"content":"vision reply"}}]}`))
	}))
	defer srv.Close()

	c := New(Options{AI: AIConfig{
		BaseURL:     srv.URL,
		APIKey:      "test-key",
		Model:       "text-model",
		VisionModel: "vision-model",
	}})

	img := base64.StdEncoding.EncodeToString([]byte("fakepng"))
	digest, err := c.Summarize(context.Background(), []Email{{
		From:    "a@example.com",
		Subject: "with image",
		Text:    "hello",
		Attachments: []Attachment{
			{Name: "screenshot.png", MimeType: "image/png", Data: []byte("fakepng")},
		},
	}})
	if err != nil {
		t.Fatal(err)
	}
	if gotModel != "vision-model" {
		t.Errorf("model = %q, want vision-model", gotModel)
	}

	parts, ok := gotContent.([]any)
	if !ok {
		t.Fatalf("user content is %T, want array of blocks", gotContent)
	}
	foundImage := false
	for _, p := range parts {
		m := p.(map[string]any)
		if m["type"] == "image_url" {
			foundImage = true
			iu := m["image_url"].(map[string]any)
			if !strings.HasPrefix(iu["url"].(string), "data:image/png;base64,"+img) {
				t.Errorf("image data URL malformed")
			}
		}
	}
	if !foundImage {
		t.Error("no image_url block in request content")
	}

	if digest.Summary != "vision reply" || digest.Images != 1 || digest.Model != "vision-model" {
		t.Errorf("digest = %+v", digest)
	}
}

func TestSummarizeFallsBackToTextOnly(t *testing.T) {
	calls := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body json.RawMessage
		json.NewDecoder(r.Body).Decode(&body)
		_, content := decodeRequest(t, body)
		calls++
		if _, isArray := content.([]any); isArray {
			// First call includes images -> model rejects them.
			w.WriteHeader(http.StatusBadRequest)
			w.Write([]byte(`{"error":{"message":"This model does not support image"}}`))
			return
		}
		w.Write([]byte(`{"choices":[{"message":{"content":"text reply"}}]}`))
	}))
	defer srv.Close()

	c := New(Options{AI: AIConfig{
		BaseURL:     srv.URL,
		APIKey:      "test-key",
		Model:       "text-model",
		VisionModel: "vision-model",
	}})

	digest, err := c.Summarize(context.Background(), []Email{{
		From:        "a@example.com",
		Subject:     "with image",
		Text:        "hello",
		Attachments: []Attachment{{Name: "x.png", MimeType: "image/png", Data: []byte("fakepng")}},
	}})
	if err != nil {
		t.Fatal(err)
	}
	if calls != 2 {
		t.Errorf("calls = %d, want 2 (vision attempt + text-only retry)", calls)
	}
	if digest.Summary != "text reply" {
		t.Errorf("summary = %q", digest.Summary)
	}
	if digest.Images != 0 || digest.ImagesSkipped != 1 || digest.Model != "text-model" {
		t.Errorf("digest = %+v", digest)
	}
}

func TestSummarizeRequiresAI(t *testing.T) {
	c := New(Options{})
	_, err := c.Summarize(context.Background(), []Email{{Subject: "x"}})
	if err == nil || !strings.Contains(err.Error(), "required") {
		t.Fatalf("expected config error, got %v", err)
	}
}

func TestSummarizeEmpty(t *testing.T) {
	c := New(Options{AI: AIConfig{BaseURL: "http://x", APIKey: "k", Model: "m"}})
	if _, err := c.Summarize(context.Background(), nil); err == nil {
		t.Fatal("expected error for empty email list")
	}
}
