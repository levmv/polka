package delivery

import (
	"bytes"
	"net/mail"
	"strings"
	"testing"
)

func TestWriteMIMEMessage(t *testing.T) {
	var buf bytes.Buffer
	err := WriteMIMEMessage(
		&buf,
		mail.Address{Name: "polka", Address: "books@example.org"},
		mail.Address{Address: "reader@kindle.com"},
		"Русская книга",
		"Sent from polka.",
		&Attachment{
			Filename:  "Книга - Автор.epub",
			MediaType: "application/epub+zip",
			Reader:    strings.NewReader("hello"),
		},
	)
	if err != nil {
		t.Fatalf("WriteMIMEMessage: %v", err)
	}
	msg := buf.String()
	for _, want := range []string{
		"From: \"polka\" <books@example.org>",
		"To: <reader@kindle.com>",
		"Subject: =?utf-8?",
		"MIME-Version: 1.0",
		"Content-Type: multipart/mixed;",
		"Content-Transfer-Encoding: base64",
		"aGVsbG8=",
	} {
		if !strings.Contains(msg, want) {
			t.Fatalf("message missing %q:\n%s", want, msg)
		}
	}
	if !strings.Contains(msg, "filename*=") {
		t.Fatalf("non-ASCII filename was not RFC2231-encoded:\n%s", msg)
	}
}

func TestBase64LineWriterWrapsAt76(t *testing.T) {
	var buf bytes.Buffer
	w := &base64LineWriter{w: &buf}
	input := strings.Repeat("A", 80)
	n, err := w.Write([]byte(input))
	if err != nil {
		t.Fatalf("write: %v", err)
	}
	if n != len(input) {
		t.Fatalf("wrote %d, want %d", n, len(input))
	}
	lines := strings.Split(buf.String(), "\r\n")
	if len(lines) != 2 || len(lines[0]) != 76 || len(lines[1]) != 4 {
		t.Fatalf("wrapped lines = %#v", lines)
	}
}
