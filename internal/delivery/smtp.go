package delivery

import (
	"context"
	"crypto/tls"
	"encoding/base64"
	"fmt"
	"io"
	"log"
	"mime"
	"mime/multipart"
	"net"
	"net/mail"
	"net/smtp"
	"net/textproto"
	"os"
	"strconv"
	"strings"
	"time"
)

const (
	SMTPSecurityAuto     = "auto"
	SMTPSecurityStartTLS = "starttls"
	SMTPSecuritySSL      = "ssl"
	SMTPSecurityPlain    = "plain"

	DefaultSMTPPort        = 587
	DefaultDeliveryTimeout = 60 * time.Second
)

type SMTPConfig struct {
	Host              string
	Port              int
	Security          string
	Username          string
	Password          string
	FromAddress       string
	FromName          string
	AttachmentLimitMB int
}

func (c SMTPConfig) Configured() bool {
	return strings.TrimSpace(c.Host) != "" && strings.TrimSpace(c.FromAddress) != ""
}

func (c SMTPConfig) Normalized() SMTPConfig {
	c.Host = strings.TrimSpace(c.Host)
	c.Username = strings.TrimSpace(c.Username)
	c.FromAddress = strings.TrimSpace(c.FromAddress)
	c.FromName = strings.TrimSpace(c.FromName)
	c.Security = strings.ToLower(strings.TrimSpace(c.Security))
	if c.Security == "" {
		c.Security = SMTPSecurityAuto
	}
	if c.Port <= 0 {
		c.Port = DefaultSMTPPort
	}
	if c.AttachmentLimitMB <= 0 {
		c.AttachmentLimitMB = DefaultAttachmentLimitMB
	}
	return c
}

func ValidSMTPSecurity(security string) bool {
	switch strings.ToLower(strings.TrimSpace(security)) {
	case "", SMTPSecurityAuto, SMTPSecurityStartTLS, SMTPSecuritySSL, SMTPSecurityPlain:
		return true
	default:
		return false
	}
}

type DeliveryCopy struct {
	Path      string
	Filename  string
	MediaType string
	Size      int64
}

type SMTPProfile struct {
	Config  SMTPConfig
	To      string
	Subject string
}

type smtpUserError struct {
	message string
	cause   error
}

func newSMTPUserError(message string, cause error) error {
	return smtpUserError{message: message, cause: cause}
}

func (e smtpUserError) Error() string {
	if e.cause == nil {
		return e.message
	}
	return e.message + ": " + e.cause.Error()
}

func (e smtpUserError) Unwrap() error {
	return e.cause
}

func (e smtpUserError) UserMessage() string {
	return e.message
}

func SendSMTP(ctx context.Context, copy DeliveryCopy, profile SMTPProfile) error {
	f, err := os.Open(copy.Path)
	if err != nil {
		return newSMTPUserError("Could not prepare file for delivery", err)
	}
	defer f.Close()
	attachment := &Attachment{
		Filename:  copy.Filename,
		MediaType: copy.MediaType,
		Reader:    f,
	}
	return sendSMTPMessage(ctx, profile.Config, profile.To, profile.Subject, "Sent from polka.", attachment)
}

func SendSMTPTest(ctx context.Context, cfg SMTPConfig, to string) error {
	return sendSMTPMessage(ctx, cfg, to, "polka email delivery test", "This is a test message from polka.", nil)
}

func sendSMTPMessage(ctx context.Context, cfg SMTPConfig, to, subject, body string, attachment *Attachment) error {
	cfg = cfg.Normalized()
	if !cfg.Configured() {
		return newSMTPUserError("Email delivery is not configured", nil)
	}
	if _, ok := ctx.Deadline(); !ok {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, DefaultDeliveryTimeout)
		defer cancel()
	}

	fromAddr, err := mail.ParseAddress(cfg.FromAddress)
	if err != nil {
		return newSMTPUserError("From address is invalid", err)
	}
	if cfg.FromName != "" {
		fromAddr.Name = cfg.FromName
	}
	toAddr, err := mail.ParseAddress(to)
	if err != nil {
		return newSMTPUserError("Device email address is invalid", err)
	}

	conn, err := dialSMTP(ctx, cfg)
	if err != nil {
		return newSMTPUserError("Could not reach the mail server", err)
	}
	defer conn.Close()
	if deadline, ok := ctx.Deadline(); ok {
		_ = conn.SetDeadline(deadline)
	}

	client, err := smtp.NewClient(conn, cfg.Host)
	if err != nil {
		return newSMTPUserError("Could not start SMTP session", err)
	}
	defer client.Close()

	wantsStartTLS := cfg.Security == SMTPSecurityStartTLS || (cfg.Security == SMTPSecurityAuto && cfg.Port != 465)
	if wantsStartTLS {
		if ok, _ := client.Extension("STARTTLS"); ok {
			tlsCfg := &tls.Config{ServerName: cfg.Host, MinVersion: tls.VersionTLS12}
			if err := client.StartTLS(tlsCfg); err != nil {
				return newSMTPUserError("Could not start TLS", err)
			}
		} else {
			return newSMTPUserError("Mail server does not support STARTTLS", nil)
		}
	}

	if cfg.Username != "" {
		if err := authenticateSMTP(client, cfg); err != nil {
			return newSMTPUserError("SMTP authentication failed", err)
		}
	}
	if err := client.Mail(fromAddr.Address); err != nil {
		return newSMTPUserError("The mail server rejected the sender", err)
	}
	if err := client.Rcpt(toAddr.Address); err != nil {
		return newSMTPUserError("The mail server rejected the recipient", err)
	}
	data, err := client.Data()
	if err != nil {
		return newSMTPUserError("The mail server rejected the message", err)
	}
	err = WriteMIMEMessage(data, *fromAddr, *toAddr, subject, body, attachment)
	closeErr := data.Close()
	if err != nil {
		return newSMTPUserError("Could not prepare email message", err)
	}
	if closeErr != nil {
		return newSMTPUserError("The mail server rejected the message", closeErr)
	}
	if err := client.Quit(); err != nil {
		log.Printf("SMTP session did not close cleanly after accepted message: %v", err)
	}
	return nil
}

func dialSMTP(ctx context.Context, cfg SMTPConfig) (net.Conn, error) {
	port := cfg.Port
	security := cfg.Security
	if security == SMTPSecurityAuto && port == 465 {
		security = SMTPSecuritySSL
	}
	addr := net.JoinHostPort(cfg.Host, strconv.Itoa(port))
	dialer := &net.Dialer{}
	raw, err := dialer.DialContext(ctx, "tcp", addr)
	if err != nil {
		return nil, err
	}
	if security != SMTPSecuritySSL {
		return raw, nil
	}
	tlsConn := tls.Client(raw, &tls.Config{ServerName: cfg.Host, MinVersion: tls.VersionTLS12})
	if err := tlsConn.HandshakeContext(ctx); err != nil {
		raw.Close()
		return nil, err
	}
	return tlsConn, nil
}

func authenticateSMTP(client *smtp.Client, cfg SMTPConfig) error {
	_, authLine := client.Extension("AUTH")
	authLine = strings.ToUpper(authLine)
	if strings.Contains(authLine, "PLAIN") {
		return client.Auth(smtp.PlainAuth("", cfg.Username, cfg.Password, cfg.Host))
	}
	if strings.Contains(authLine, "LOGIN") {
		return client.Auth(loginAuth{username: cfg.Username, password: cfg.Password})
	}
	return fmt.Errorf("mail server does not advertise a supported auth method")
}

type loginAuth struct {
	username string
	password string
}

func (a loginAuth) Start(server *smtp.ServerInfo) (string, []byte, error) {
	if server == nil || !server.TLS {
		return "", nil, fmt.Errorf("LOGIN authentication requires TLS")
	}
	return "LOGIN", []byte(a.username), nil
}

func (a loginAuth) Next(_ []byte, more bool) ([]byte, error) {
	if !more {
		return nil, nil
	}
	return []byte(a.password), nil
}

type Attachment struct {
	Filename  string
	MediaType string
	Reader    io.Reader
}

func WriteMIMEMessage(w io.Writer, from, to mail.Address, subject, body string, attachment *Attachment) error {
	if strings.TrimSpace(subject) == "" {
		subject = "polka"
	}
	if strings.TrimSpace(body) == "" {
		body = "Sent from polka."
	}

	mw := multipart.NewWriter(w)
	headers := []string{
		"From: " + from.String(),
		"To: " + to.String(),
		"Subject: " + mime.QEncoding.Encode("utf-8", subject),
		"Date: " + time.Now().UTC().Format(time.RFC1123Z),
		"MIME-Version: 1.0",
		"Content-Type: " + mime.FormatMediaType("multipart/mixed", map[string]string{"boundary": mw.Boundary()}),
		"",
	}
	if _, err := io.WriteString(w, strings.Join(headers, "\r\n")+"\r\n"); err != nil {
		return err
	}

	textHeader := make(textproto.MIMEHeader)
	textHeader.Set("Content-Type", mime.FormatMediaType("text/plain", map[string]string{"charset": "utf-8"}))
	textHeader.Set("Content-Transfer-Encoding", "8bit")
	textPart, err := mw.CreatePart(textHeader)
	if err != nil {
		return err
	}
	if _, err := io.WriteString(textPart, body+"\r\n"); err != nil {
		return err
	}
	if attachment == nil || attachment.Reader == nil {
		return mw.Close()
	}
	if attachment.MediaType == "" {
		attachment.MediaType = "application/octet-stream"
	}
	if attachment.Filename == "" {
		attachment.Filename = "book"
	}

	attachmentHeader := make(textproto.MIMEHeader)
	attachmentHeader.Set("Content-Type", mime.FormatMediaType(attachment.MediaType, map[string]string{"name": attachment.Filename}))
	attachmentHeader.Set("Content-Disposition", mime.FormatMediaType("attachment", map[string]string{"filename": attachment.Filename}))
	attachmentHeader.Set("Content-Transfer-Encoding", "base64")
	attachmentPart, err := mw.CreatePart(attachmentHeader)
	if err != nil {
		return err
	}
	lineWriter := &base64LineWriter{w: attachmentPart}
	encoder := base64.NewEncoder(base64.StdEncoding, lineWriter)
	if _, err := io.Copy(encoder, attachment.Reader); err != nil {
		encoder.Close()
		return err
	}
	if err := encoder.Close(); err != nil {
		return err
	}
	if lineWriter.col != 0 {
		if _, err := io.WriteString(attachmentPart, "\r\n"); err != nil {
			return err
		}
	}
	return mw.Close()
}

type base64LineWriter struct {
	w   io.Writer
	col int
}

func (w *base64LineWriter) Write(p []byte) (int, error) {
	written := 0
	for len(p) > 0 {
		space := 76 - w.col
		if space == 0 {
			if _, err := io.WriteString(w.w, "\r\n"); err != nil {
				return written, err
			}
			w.col = 0
			space = 76
		}
		if space > len(p) {
			space = len(p)
		}
		n, err := w.w.Write(p[:space])
		written += n
		w.col += n
		p = p[n:]
		if err != nil {
			return written, err
		}
		if n == 0 {
			return written, io.ErrShortWrite
		}
	}
	return written, nil
}
