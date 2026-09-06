package delivery

import (
	"bytes"
	"context"
	"encoding/base64"
	"errors"
	"io"
	"mime"
	"mime/multipart"
	"net"
	"net/mail"
	"net/textproto"
	"strconv"
	"strings"
	"testing"
	"time"
)

func TestSendSMTPMessage(t *testing.T) {
	for _, tc := range []struct {
		name          string
		rejectMessage bool
		brokenRead    bool
	}{
		{name: "accepted"},
		{name: "message rejected", rejectMessage: true},
		{name: "attachment read fails", brokenRead: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			listener, err := net.Listen("tcp", "127.0.0.1:0")
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { listener.Close() })
			type result struct {
				message []byte
				err     error
			}
			done := make(chan result, 1)
			go func() {
				conn, err := listener.Accept()
				if err != nil {
					done <- result{err: err}
					return
				}
				defer conn.Close()
				conn.SetDeadline(time.Now().Add(5 * time.Second))
				wire := textproto.NewConn(conn)
				wire.PrintfLine("220 localhost ESMTP")
				var message []byte
				for {
					line, err := wire.ReadLine()
					if errors.Is(err, io.EOF) {
						done <- result{message: message}
						return
					}
					if err != nil {
						done <- result{err: err}
						return
					}
					switch {
					case strings.HasPrefix(line, "EHLO "), strings.HasPrefix(line, "HELO "),
						strings.HasPrefix(line, "MAIL FROM:"), strings.HasPrefix(line, "RCPT TO:"):
						wire.PrintfLine("250 OK")
					case line == "DATA":
						wire.PrintfLine("354 Send message")
						data, err := io.ReadAll(wire.DotReader())
						if err != nil {
							if tc.brokenRead && errors.Is(err, io.ErrUnexpectedEOF) {
								err = nil // An aborted DATA must never become a complete message.
							}
							done <- result{err: err}
							return
						}
						message = data
						if tc.rejectMessage {
							wire.PrintfLine("552 Message rejected")
						} else {
							wire.PrintfLine("250 Accepted")
						}
					case line == "QUIT":
						wire.PrintfLine("221 Bye")
						done <- result{message: message}
						return
					default:
						done <- result{err: errors.New("unexpected SMTP command: " + line)}
						return
					}
				}
			}()
			host, port, _ := net.SplitHostPort(listener.Addr().String())
			portNumber, _ := strconv.Atoi(port)
			var reader io.Reader = strings.NewReader("complete book bytes")
			if tc.brokenRead {
				reader = io.MultiReader(reader, smtpBrokenReader{})
			}
			err = sendSMTPMessage(context.Background(), SMTPConfig{
				Host: host, Port: portNumber, Security: SMTPSecurityPlain,
				FromAddress: "books@example.org",
			}, "reader@example.org", "A book", "Sent from polka.", &Attachment{
				Filename: "book.epub", MediaType: "application/epub+zip", Reader: reader,
			})
			wantError := tc.rejectMessage || tc.brokenRead
			if (err != nil) != wantError {
				t.Fatalf("send error = %v; want error %v", err, wantError)
			}
			got := <-done
			if got.err != nil {
				t.Fatal(got.err)
			}
			if tc.brokenRead {
				if len(got.message) != 0 {
					t.Fatal("server received a complete message after an attachment read error")
				}
				return
			}
			msg, err := mail.ReadMessage(bytes.NewReader(got.message))
			if err != nil {
				t.Fatal(err)
			}
			_, params, err := mime.ParseMediaType(msg.Header.Get("Content-Type"))
			if err != nil {
				t.Fatal(err)
			}
			parts := multipart.NewReader(msg.Body, params["boundary"])
			if _, err := parts.NextPart(); err != nil {
				t.Fatal(err)
			}
			attachment, err := parts.NextPart()
			if err != nil {
				t.Fatal(err)
			}
			data, err := io.ReadAll(base64.NewDecoder(base64.StdEncoding, attachment))
			if err != nil || string(data) != "complete book bytes" || attachment.FileName() != "book.epub" {
				t.Fatalf("attachment = %q, %q, %v", attachment.FileName(), data, err)
			}
		})
	}
}

type smtpBrokenReader struct{}

func (smtpBrokenReader) Read([]byte) (int, error) { return 0, errors.New("source read failed") }

func TestSendSMTPMessageCancellationClosesConnection(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	listener.(*net.TCPListener).SetDeadline(time.Now().Add(5 * time.Second))
	host, port, _ := net.SplitHostPort(listener.Addr().String())
	portNumber, _ := strconv.Atoi(port)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() {
		done <- SendSMTPTest(ctx, SMTPConfig{
			Host: host, Port: portNumber, Security: SMTPSecurityPlain,
			FromAddress: "books@example.org",
		}, "reader@example.org")
	}()
	conn, err := listener.Accept()
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	// Withhold the greeting: cancellation must interrupt an established SMTP read.
	cancel()
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("cancelled SMTP session succeeded")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("SMTP ignored cancellation while waiting for the server")
	}
}

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
