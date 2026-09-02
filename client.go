// Package mailai reads email from an IMAP mailbox and summarizes it with any
// OpenAI-compatible LLM API (DeepSeek, OpenAI, Ollama, vLLM, ...).
//
// The library is deliberately read-only: mailboxes are selected read-only and
// message bodies are fetched with PEEK, so mailai never changes message flags.
// In particular, it never marks emails as read.
package mailai

import (
	"context"
	"net/http"
	"time"
)

// IMAPConfig holds the connection settings for the mail server.
type IMAPConfig struct {
	// Host is the mail server address, optionally with a port
	// ("mail.example.com" or "mail.example.com:993"). When no port is
	// given, 993 (implicit TLS) is assumed. Port 143 selects a plaintext
	// connection.
	Host string
	// Username is the login name, usually the full email address.
	Username string
	// Password is the mailbox password (or an app-specific password).
	Password string
	// InsecureSkipVerify disables TLS certificate verification. Intended
	// only for servers with self-signed certificates or hostname
	// mismatches; prefer fixing the certificate instead. It is ignored
	// for plaintext connections.
	InsecureSkipVerify bool
}

// AIConfig holds the settings for the OpenAI-compatible LLM endpoint.
type AIConfig struct {
	// BaseURL is the API root, e.g. "https://api.deepseek.com". The
	// library appends "/chat/completions".
	BaseURL string
	// APIKey is the bearer token sent in the Authorization header.
	APIKey string
	// Model is the text model used for summarization, e.g.
	// "deepseek-v4-flash".
	Model string
	// VisionModel is the model used when image attachments are analyzed,
	// e.g. "deepseek-v4-flash-vision-exp". If empty, images are skipped.
	VisionModel string
	// SystemPrompt overrides the default summarization instructions.
	SystemPrompt string
	// HTTPClient is the client used for API calls. If nil, a client with
	// a 3-minute timeout is used.
	HTTPClient *http.Client
}

// Options configures a Client.
type Options struct {
	// IMAP is the mail server configuration.
	IMAP IMAPConfig
	// AI is the LLM configuration.
	AI AIConfig
	// Mailbox is the mailbox to read. Defaults to "INBOX".
	Mailbox string
	// MaxBodyChars caps the email text sent to the model per email.
	// Defaults to DefaultMaxBodyChars.
	MaxBodyChars int
	// MaxPDFChars caps the extracted text per PDF sent to the model.
	// Defaults to DefaultMaxPDFChars.
	MaxPDFChars int
	// MaxImages caps the number of images sent per request. Defaults to
	// DefaultMaxImages.
	MaxImages int
	// DisablePDFs turns off PDF text extraction.
	DisablePDFs bool
	// DisableImages turns off image analysis (equivalent to leaving
	// VisionModel empty).
	DisableImages bool
}

// Default limits used when Options fields are zero.
const (
	DefaultMaxBodyChars = 1500
	DefaultMaxPDFChars  = 2000
	DefaultMaxImages    = 3
)

// Email is a single fetched message.
type Email struct {
	// SeqNum is the message sequence number in the mailbox at fetch time.
	SeqNum uint32
	// UID is the server-assigned unique identifier.
	UID   uint32
	// Date is the message date.
	Date  time.Time
	// From is the sender address.
	From  string
	// To lists the primary recipients.
	To    []string
	// Cc lists the carbon-copy recipients.
	Cc    []string
	// Subject is the message subject.
	Subject string
	// Text is the extracted plain-text body (HTML is converted).
	Text  string
	// Attachments holds decoded attachments (PDFs, images, ...).
	Attachments []Attachment
}

// Attachment is a decoded MIME attachment.
type Attachment struct {
	// Name is the file name (RFC 2047 decoded).
	Name string
	// MimeType is the declared media type, e.g. "application/pdf".
	MimeType string
	// Data is the decoded content.
	Data []byte
}

// Digest is the result of an AI summarization.
type Digest struct {
	// Summary is the model's response.
	Summary string
	// Model is the model that produced the response.
	Model string
	// Emails is the number of emails summarized.
	Emails int
	// PDFs is the number of PDFs whose text was included.
	PDFs int
	// Images is the number of images actually analyzed by the model.
	Images int
	// ImagesSkipped is the number of images that could not be analyzed
	// (no vision model, unsupported format, or model rejection).
	ImagesSkipped int
}

// Client connects an IMAP mailbox to an LLM endpoint.
type Client struct {
	imap IMAPConfig
	ai   AIConfig
	opts Options
}

const defaultSystemPrompt = "You are an assistant that reviews business emails. " +
	"Be concise and practical: give a one-line summary per email, the key points, " +
	"pending actions, and any relevant details from attachments (PDFs and images)."

// New creates a Client with the given options, applying defaults for zero
// values. Options are validated lazily by the operations that need them, so a
// Client can be used only for Summarize without any IMAP configuration.
func New(opts Options) *Client {
	if opts.Mailbox == "" {
		opts.Mailbox = "INBOX"
	}
	if opts.MaxBodyChars <= 0 {
		opts.MaxBodyChars = DefaultMaxBodyChars
	}
	if opts.MaxPDFChars <= 0 {
		opts.MaxPDFChars = DefaultMaxPDFChars
	}
	if opts.MaxImages <= 0 {
		opts.MaxImages = DefaultMaxImages
	}
	return &Client{imap: opts.IMAP, ai: opts.AI, opts: opts}
}

// FetchRecent fetches the n newest emails from the mailbox (n <= 0 means all
// messages). It is read-only: nothing on the server is modified and no
// message is marked as read. The context cannot cancel the IMAP connection
// (a limitation of the underlying IMAP client); it applies to the AI calls
// in DigestRecent.
func (c *Client) FetchRecent(ctx context.Context, n int) ([]Email, error) {
	if err := c.validateIMAP(); err != nil {
		return nil, err
	}
	return c.fetch(ctx, n)
}

// FetchSince fetches the messages dated at or after since, newest first, up
// to max messages (max <= 0 means no cap beyond the mailbox size). A zero
// since returns the whole mailbox. Like every read in this package, it never
// changes message flags.
func (c *Client) FetchSince(ctx context.Context, since time.Time, max int) ([]Email, error) {
	if err := c.validateIMAP(); err != nil {
		return nil, err
	}
	return c.fetchSince(ctx, since, max)
}

// Summarize sends the given emails to the LLM. PDF text and images are
// included according to the options; the vision model is used automatically
// when images are present and a VisionModel is configured.
func (c *Client) Summarize(ctx context.Context, emails []Email) (*Digest, error) {
	if err := c.validateAI(); err != nil {
		return nil, err
	}
	return c.summarize(ctx, emails)
}

// DigestRecent fetches the n newest emails and summarizes them in one call.
func (c *Client) DigestRecent(ctx context.Context, n int) (*Digest, error) {
	emails, err := c.FetchRecent(ctx, n)
	if err != nil {
		return nil, err
	}
	return c.Summarize(ctx, emails)
}

// DigestRecent is a one-shot convenience equivalent to
// New(opts).DigestRecent(ctx, n).
func DigestRecent(ctx context.Context, opts Options, n int) (*Digest, error) {
	return New(opts).DigestRecent(ctx, n)
}

func (c *Client) validateIMAP() error {
	switch {
	case c.imap.Host == "":
		return errMissing("IMAP host")
	case c.imap.Username == "":
		return errMissing("IMAP username")
	case c.imap.Password == "":
		return errMissing("IMAP password")
	}
	return nil
}

func (c *Client) validateAI() error {
	switch {
	case c.ai.BaseURL == "":
		return errMissing("AI base URL")
	case c.ai.APIKey == "":
		return errMissing("AI API key")
	case c.ai.Model == "":
		return errMissing("AI model")
	}
	return nil
}

func (c *Client) systemPrompt() string {
	if c.ai.SystemPrompt != "" {
		return c.ai.SystemPrompt
	}
	return defaultSystemPrompt
}

func errMissing(field string) error {
	return &ConfigError{Field: field}
}

// ConfigError reports a missing required configuration field.
type ConfigError struct {
	Field string
}

func (e *ConfigError) Error() string {
	return "mailai: " + e.Field + " is required"
}
