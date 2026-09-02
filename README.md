# mailai

**Read and summarize your inbox with any OpenAI-compatible LLM — safely.**

`mailai` is a small Go library that connects an IMAP mailbox to an LLM API
(DeepSeek, OpenAI, Ollama, vLLM, ...). Give it server credentials and an API
key, and it fetches recent emails, extracts the text, pulls content out of
PDF attachments, and — when a vision model is configured — sends image
attachments to the model for analysis. The result is one digest.

```go
digest, err := mailai.DigestRecent(ctx, mailai.Options{
    IMAP: mailai.IMAPConfig{
        Host:     "mail.example.com:993",
        Username: "me@example.com",
        Password: os.Getenv("MAIL_PASSWORD"),
    },
    AI: mailai.AIConfig{
        BaseURL:     "https://api.deepseek.com",
        APIKey:      os.Getenv("DEEPSEEK_API_KEY"),
        Model:       "deepseek-v4-flash",
        VisionModel: "deepseek-v4-flash-vision-exp",
    },
}, 20) // last 20 emails
```

## Why mailai?

Assembling an "inbox → LLM digest" pipeline by hand means dealing with the
traps of both worlds:

- accidentally **marking emails as read** while fetching them,
- MIME soup: `text/plain` vs `text/html`, quoted-printable, base64,
  RFC 2047 filenames,
- self-signed certificates on cPanel-style hosts,
- text models that reject images, vision models that cost more,
- PDF attachments nobody can read without extra tooling.

`mailai` encapsulates all of that behind one small API.

## Read-only guarantee

**`mailai` never modifies your mailbox.** It selects mailboxes in read-only
mode and fetches message bodies with `PEEK`, so no message is ever marked as
read and no flags change. This is a design guarantee, not an option — there
is no write path in the library.

## Features

- Read recent emails (newest first) or the whole mailbox
- Extracts plain-text bodies (prefers `text/plain`, falls back to
  `text/html` converted to text)
- Decodes quoted-printable / base64 transfer encodings and RFC 2047 headers
- Extracts text from **PDF attachments** and includes it in the prompt
- Sends **image attachments** to a vision model (base64 data URLs)
- Automatic **model switching**: the fast text model is used normally, the
  vision model only when images are present
- Graceful fallback to a text-only request when the model rejects images
- Any OpenAI-compatible endpoint: DeepSeek, OpenAI, Ollama, vLLM, ...

## Installation

```sh
go get github.com/samuelcampozano/mailai
```

For local development with an unpublished checkout, use a `replace`
directive:

```sh
go mod edit -replace github.com/samuelcampozano/mailai=../mailai
```

## Usage

```go
package main

import (
    "context"
    "fmt"
    "log"
    "os"

    "github.com/samuelcampozano/mailai"
)

func main() {
    c := mailai.New(mailai.Options{
        IMAP: mailai.IMAPConfig{
            Host:     os.Getenv("IMAP_HOST"), // "mail.example.com:993"
            Username: os.Getenv("IMAP_USER"),
            Password: os.Getenv("IMAP_PASSWORD"),
        },
        AI: mailai.AIConfig{
            BaseURL:     "https://api.deepseek.com",
            APIKey:      os.Getenv("AI_API_KEY"),
            Model:       "deepseek-v4-flash",
            VisionModel: "deepseek-v4-flash-vision-exp",
        },
    })

    // Fetch + summarize in one call.
    digest, err := c.DigestRecent(context.Background(), 10)
    if err != nil {
        log.Fatal(err)
    }
    fmt.Println(digest.Summary)

    // Or fetch first, then summarize with your own prompt.
    emails, err := c.FetchRecent(context.Background(), 10)
    // ... filter, inspect, or store emails ...
    digest, err = c.Summarize(context.Background(), emails)
}
```

### Configuration

| Field | Default | Meaning |
|---|---|---|
| `Options.Mailbox` | `"INBOX"` | Mailbox to read |
| `Options.MaxBodyChars` | `1500` | Text cap per email sent to the model |
| `Options.MaxPDFChars` | `2000` | Extracted-text cap per PDF |
| `Options.MaxImages` | `3` | Images per request |
| `Options.DisablePDFs` | `false` | Turn off PDF text extraction |
| `Options.DisableImages` | `false` | Turn off image analysis |
| `AIConfig.SystemPrompt` | built-in | Custom instructions for the model |
| `AIConfig.HTTPClient` | 3-min timeout | Custom HTTP client |
| `IMAPConfig.InsecureSkipVerify` | `false` | Allow self-signed certificates |

Port rules: no port in `Host` → `993` (implicit TLS); port `143` →
plaintext. The AI endpoint used is `{BaseURL}/chat/completions`.

### The Digest

```go
type Digest struct {
    Summary       string // the model's response
    Model         string // model that produced it
    Emails        int    // emails summarized
    PDFs          int    // PDFs included
    Images        int    // images actually analyzed
    ImagesSkipped int    // images that could not be analyzed
}
```

## Security

- **Credentials never leave your code**: the library takes config in code;
  read credentials from environment variables or a secret manager.
- **Never commit credentials.** `.env` files belong in `.gitignore`.
- TLS is required for connections other than port 143, with TLS 1.2 as the
  minimum version.
- `InsecureSkipVerify` exists for self-signed servers (common with cPanel
  hosts addressed by IP). Prefer fixing the certificate.
- The API key is sent only to the configured `BaseURL` over HTTPS.
- Email content is sent to the LLM provider you configure — review your
  provider's data policy before pointing this at sensitive mailboxes.

## Limitations

- The underlying IMAP client (go-imap v1) cannot cancel in-flight IMAP
  commands via `context`; contexts apply to the AI calls.
- Scanned PDFs without a text layer yield no text (OCR is out of scope).
- Only JPEG, PNG, GIF and WebP images are sent to the vision model.
- Broken MIME structures are handled best-effort; text may occasionally be
  missing for exotic messages.

## Testing

The test suite runs fully offline: MIME parsing tests, an OpenAI-compatible
endpoint stub, and an in-memory IMAP server that verifies the read-only
guarantee (no `\Seen` flags, zero flag-modifying commands).

```sh
go test ./...
```

## Author

**Samuel Campozano Lopez**

- GitHub: https://github.com/samuelcampozano
- LinkedIn: https://www.linkedin.com/in/samuel-campozano-lopez/

## License

MIT — see [LICENSE](LICENSE).
