package mailai

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"

	"github.com/emersion/go-imap/server"
)

// Example shows the full pipeline — IMAP fetch plus AI summarization — using
// only in-memory servers, so it runs offline and requires no credentials.
func Example() {
	// 1. In-memory IMAP server with one message.
	mbox := &testMailbox{messages: []*testMessage{{uid: 1, raw: []byte(testRawEmail)}}}
	be := &testBackend{user: &testUser{mbox: mbox}}
	s := server.New(be)
	s.AllowInsecureAuth = true
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		fmt.Println("listen:", err)
		return
	}
	go s.Serve(l)
	defer func() { s.Close(); l.Close() }()

	// 2. Fake OpenAI-compatible endpoint.
	aiSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"choices":[{"message":{"content":"AI reply here"}}]}`))
	}))
	defer aiSrv.Close()

	// 3. One call: fetch and summarize.
	forcePlaintext = true
	defer func() { forcePlaintext = false }()

	digest, err := DigestRecent(context.Background(), Options{
		IMAP: IMAPConfig{Host: l.Addr().String(), Username: "user", Password: "pass"},
		AI:   AIConfig{BaseURL: aiSrv.URL, APIKey: "test-key", Model: "test-model"},
	}, 10)
	if err != nil {
		fmt.Println("error:", err)
		return
	}
	fmt.Println(digest.Summary)
	// Output: AI reply here
}
