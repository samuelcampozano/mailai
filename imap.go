package mailai

import (
	"context"
	"crypto/tls"
	"fmt"
	"io"
	"net"
	"strings"

	"github.com/emersion/go-imap"
	imapclient "github.com/emersion/go-imap/client"
)

// forcePlaintext is a test hook that forces an unencrypted connection
// regardless of the port. It is not part of the public API.
var forcePlaintext = false

// dial connects to the IMAP server. Port 143 selects a plaintext connection;
// anything else uses implicit TLS.
func (c *Client) dial() (*imapclient.Client, error) {
	host := c.imap.Host
	if _, _, err := net.SplitHostPort(host); err != nil {
		host = net.JoinHostPort(host, "993")
	}
	hostname, port, err := net.SplitHostPort(host)
	if err != nil {
		return nil, fmt.Errorf("mailai: invalid IMAP host %q: %w", c.imap.Host, err)
	}

	if !forcePlaintext && port != "143" {
		tlsCfg := &tls.Config{
			ServerName:         hostname,
			MinVersion:         tls.VersionTLS12,
			InsecureSkipVerify: c.imap.InsecureSkipVerify,
		}
		return imapclient.DialTLS(host, tlsCfg)
	}
	return imapclient.Dial(host)
}

// fetch fetches the n newest emails (n <= 0 means all), newest first.
// The mailbox is always selected read-only and bodies are fetched with PEEK,
// so the server never changes message flags.
func (c *Client) fetch(ctx context.Context, n int) ([]Email, error) {
	conn, err := c.dial()
	if err != nil {
		return nil, fmt.Errorf("mailai: dial: %w", err)
	}
	defer conn.Logout()

	if err := conn.Login(c.imap.Username, c.imap.Password); err != nil {
		return nil, fmt.Errorf("mailai: login: %w", err)
	}

	// readOnly=true: the server rejects any attempt to modify flags.
	mbox, err := conn.Select(c.opts.Mailbox, true)
	if err != nil {
		return nil, fmt.Errorf("mailai: select %q: %w", c.opts.Mailbox, err)
	}
	if mbox.Messages == 0 {
		return nil, nil
	}

	seqset := new(imap.SeqSet)
	start := uint32(1)
	if n > 0 && mbox.Messages > uint32(n) {
		start = mbox.Messages - uint32(n) + 1
	}
	seqset.AddRange(start, mbox.Messages)

	// Peek=true: fetch the body WITHOUT setting the \Seen flag.
	section := &imap.BodySectionName{Peek: true}
	items := []imap.FetchItem{imap.FetchEnvelope, imap.FetchUid, section.FetchItem()}

	messages := make(chan *imap.Message, 16)
	done := make(chan error, 1)
	go func() { done <- conn.Fetch(seqset, items, messages) }()

	var out []Email
	for msg := range messages {
		from := ""
		if len(msg.Envelope.From) > 0 {
			from = msg.Envelope.From[0].Address()
		}
		var raw []byte
		if lit := msg.GetBody(section); lit != nil {
			raw, _ = io.ReadAll(lit)
		}
		text, atts := parseMessage(raw)
		out = append(out, Email{
			SeqNum:      msg.SeqNum,
			UID:         msg.Uid,
			Date:        msg.Envelope.Date,
			From:        from,
			To:          addrStrings(msg.Envelope.To),
			Cc:          addrStrings(msg.Envelope.Cc),
			Subject:     msg.Envelope.Subject,
			Text:        text,
			Attachments: atts,
		})
	}
	if err := <-done; err != nil {
		return nil, fmt.Errorf("mailai: fetch: %w", err)
	}

	// Newest first.
	for i, j := 0, len(out)-1; i < j; i, j = i+1, j-1 {
		out[i], out[j] = out[j], out[i]
	}
	return out, nil
}

func addrStrings(addrs []*imap.Address) []string {
	out := make([]string, 0, len(addrs))
	for _, a := range addrs {
		if a == nil || strings.TrimSpace(a.Address()) == "" {
			continue
		}
		out = append(out, a.Address())
	}
	return out
}
