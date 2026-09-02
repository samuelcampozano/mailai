package mailai

import (
	"bytes"
	"context"
	"io"
	"net"
	"net/mail"
	"strings"
	"testing"
	"time"

	"github.com/emersion/go-imap"
	"github.com/emersion/go-imap/backend"
	"github.com/emersion/go-imap/server"
)

// ---------------------------------------------------------------------------
// Minimal in-memory IMAP backend used only by the tests. It gives the test
// full control over message flags so the read-only guarantee can be verified.
// ---------------------------------------------------------------------------

type testBackend struct{ user backend.User }

func (tb *testBackend) Login(_ *imap.ConnInfo, username, password string) (backend.User, error) {
	if username == "user" && password == "pass" {
		return tb.user, nil
	}
	return nil, backend.ErrInvalidCredentials
}

type testUser struct{ mbox *testMailbox }

func (u *testUser) Username() string                                { return "user" }
func (u *testUser) ListMailboxes(bool) ([]backend.Mailbox, error)   { return []backend.Mailbox{u.mbox}, nil }
func (u *testUser) GetMailbox(name string) (backend.Mailbox, error) {
	if name == "INBOX" {
		return u.mbox, nil
	}
	return nil, backend.ErrNoSuchMailbox
}
func (u *testUser) CreateMailbox(string) error                        { return nil }
func (u *testUser) DeleteMailbox(string) error                        { return nil }
func (u *testUser) RenameMailbox(string, string) error                { return nil }
func (u *testUser) Logout() error                                     { return nil }

type testMessage struct {
	uid   uint32
	raw   []byte
	flags []string
}

// testLiteral adapts []byte to imap.Literal for the test backend.
type testLiteral struct {
	r *bytes.Reader
	n int
}

func (l *testLiteral) Read(p []byte) (int, error) { return l.r.Read(p) }
func (l *testLiteral) Len() int                   { return l.n }

func asLiteral(b []byte) imap.Literal {
	return &testLiteral{r: bytes.NewReader(b), n: len(b)}
}

type testMailbox struct {
	messages []*testMessage
	storeOps int // counts flag modifications (should stay 0 in read-only)
}

func (m *testMailbox) Name() string                      { return "INBOX" }
func (m *testMailbox) Info() (*imap.MailboxInfo, error) {
	return &imap.MailboxInfo{Name: "INBOX", Delimiter: "/"}, nil
}
func (m *testMailbox) Status(items []imap.StatusItem) (*imap.MailboxStatus, error) {
	st := imap.NewMailboxStatus("INBOX", items)
	st.Flags = []string{imap.SeenFlag}
	st.PermanentFlags = []string{imap.SeenFlag}
	st.Messages = uint32(len(m.messages))
	st.UidValidity = 1
	st.UidNext = uint32(len(m.messages)) + 1
	st.UnseenSeqNum = 1
	return st, nil
}
func (m *testMailbox) SetSubscribed(bool) error { return nil }
func (m *testMailbox) Check() error             { return nil }

func (m *testMailbox) ListMessages(uid bool, seqset *imap.SeqSet, items []imap.FetchItem, ch chan<- *imap.Message) error {
	defer close(ch)
	for i, tm := range m.messages {
		seqNum := uint32(i + 1)
		if uid {
			if !seqset.Contains(tm.uid) {
				continue
			}
		} else if !seqset.Contains(seqNum) {
			continue
		}

		fetched := imap.NewMessage(seqNum, items)
		fetched.Uid = tm.uid
		for _, item := range items {
			switch item {
			case imap.FetchEnvelope:
				fetched.Envelope = testEnvelope(tm.raw)
			case imap.FetchFlags:
				fetched.Flags = append([]string{}, tm.flags...)
			case imap.FetchInternalDate:
				fetched.InternalDate = time.Now()
			case imap.FetchRFC822Size:
				fetched.Size = uint32(len(tm.raw))
			default:
				if section, err := imap.ParseBodySectionName(item); err == nil && len(section.Path) == 0 {
					if fetched.Body == nil {
						fetched.Body = map[*imap.BodySectionName]imap.Literal{}
					}
					fetched.Body[section] = asLiteral(tm.raw)
				}
			}
		}
		ch <- fetched
	}
	return nil
}

func testEnvelope(raw []byte) *imap.Envelope {
	msg, err := mail.ReadMessage(bytes.NewReader(raw))
	if err != nil {
		return nil
	}
	hdr := msg.Header
	date, _ := mail.ParseDate(hdr.Get("Date"))
	return &imap.Envelope{
		Date:      date,
		Subject:   hdr.Get("Subject"),
		From:      testAddrs(hdr.Get("From")),
		To:        testAddrs(hdr.Get("To")),
		Cc:        testAddrs(hdr.Get("Cc")),
		MessageId: hdr.Get("Message-Id"),
	}
}

func testAddrs(s string) []*imap.Address {
	if s == "" {
		return nil
	}
	list, err := mail.ParseAddressList(s)
	if err != nil {
		return nil
	}
	out := make([]*imap.Address, 0, len(list))
	for _, a := range list {
		addr := &imap.Address{PersonalName: a.Name}
		if i := strings.LastIndex(a.Address, "@"); i > 0 {
			addr.MailboxName = a.Address[:i]
			addr.HostName = a.Address[i+1:]
		}
		out = append(out, addr)
	}
	return out
}

func (m *testMailbox) SearchMessages(bool, *imap.SearchCriteria) ([]uint32, error) { return nil, nil }
func (m *testMailbox) CreateMessage(flags []string, date time.Time, body imap.Literal) error {
	data, _ := io.ReadAll(body)
	m.messages = append(m.messages, &testMessage{uid: uint32(len(m.messages)) + 1, raw: data, flags: flags})
	return nil
}
func (m *testMailbox) UpdateMessagesFlags(bool, *imap.SeqSet, imap.FlagsOp, []string) error {
	m.storeOps++
	return nil
}
func (m *testMailbox) CopyMessages(bool, *imap.SeqSet, string) error { return nil }
func (m *testMailbox) Expunge() error                                { return nil }

// startIMAPServer runs an in-memory IMAP server on a random local port.
func startIMAPServer(t *testing.T, messages ...*testMessage) (addr string, mbox *testMailbox) {
	t.Helper()
	mbox = &testMailbox{messages: messages}
	be := &testBackend{user: &testUser{mbox: mbox}}
	s := server.New(be)
	s.AllowInsecureAuth = true

	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	go s.Serve(l)
	t.Cleanup(func() {
		s.Close()
		l.Close()
	})
	return l.Addr().String(), mbox
}

// ---------------------------------------------------------------------------
// Tests
// ---------------------------------------------------------------------------

const testRawEmail = "From: sender@example.com\r\n" +
	"To: receiver@example.com\r\n" +
	"Cc: cc@example.com\r\n" +
	"Subject: Test email\r\n" +
	"Date: Tue, 01 Sep 2026 10:00:00 +0000\r\n" +
	"Message-Id: <123@example.com>\r\n" +
	"MIME-Version: 1.0\r\n" +
	"Content-Type: text/plain; charset=utf-8\r\n" +
	"\r\n" +
	"Hello from the test inbox."

func TestFetchRecentIsReadOnly(t *testing.T) {
	addr, mbox := startIMAPServer(t, &testMessage{uid: 7, raw: []byte(testRawEmail)})

	forcePlaintext = true
	t.Cleanup(func() { forcePlaintext = false })

	c := New(Options{IMAP: IMAPConfig{
		Host:     addr,
		Username: "user",
		Password: "pass",
	}})

	emails, err := c.FetchRecent(context.Background(), 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(emails) != 1 {
		t.Fatalf("got %d emails, want 1", len(emails))
	}

	e := emails[0]
	if e.UID != 7 {
		t.Errorf("uid = %d, want 7", e.UID)
	}
	if e.Subject != "Test email" {
		t.Errorf("subject = %q", e.Subject)
	}
	if e.From != "sender@example.com" {
		t.Errorf("from = %q", e.From)
	}
	if len(e.To) != 1 || e.To[0] != "receiver@example.com" {
		t.Errorf("to = %v", e.To)
	}
	if !strings.Contains(e.Text, "Hello from the test inbox.") {
		t.Errorf("text = %q", e.Text)
	}

	// The read-only guarantee: no \Seen flag may have been added.
	for _, f := range mbox.messages[0].flags {
		if f == imap.SeenFlag {
			t.Fatal("fetch marked the message as seen!")
		}
	}
	if mbox.storeOps != 0 {
		t.Fatalf("server received %d flag-modifying commands, want 0", mbox.storeOps)
	}
}

func TestFetchRecentLimit(t *testing.T) {
	addr, _ := startIMAPServer(t,
		&testMessage{uid: 1, raw: []byte(testRawEmail)},
		&testMessage{uid: 2, raw: []byte(testRawEmail)},
		&testMessage{uid: 3, raw: []byte(testRawEmail)},
	)

	forcePlaintext = true
	t.Cleanup(func() { forcePlaintext = false })

	c := New(Options{IMAP: IMAPConfig{Host: addr, Username: "user", Password: "pass"}})
	emails, err := c.FetchRecent(context.Background(), 2)
	if err != nil {
		t.Fatal(err)
	}
	if len(emails) != 2 {
		t.Fatalf("got %d emails, want 2", len(emails))
	}
	// Newest first: the last two messages.
	if emails[0].UID != 3 || emails[1].UID != 2 {
		t.Errorf("unexpected order: %d, %d", emails[0].UID, emails[1].UID)
	}
}

func TestFetchRecentMissingIMAPConfig(t *testing.T) {
	c := New(Options{})
	if _, err := c.FetchRecent(context.Background(), 10); err == nil {
		t.Fatal("expected config error")
	}
}
