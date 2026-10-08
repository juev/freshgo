// Package mail sends the few letters freshgo writes, as plain text through
// an SMTP server.
package mail

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/tls"
	"encoding/hex"
	"errors"
	"fmt"
	"mime"
	"mime/quotedprintable"
	"net"
	netmail "net/mail"
	"net/smtp"
	"net/url"
	"strings"
	"time"
)

// timeout bounds the sending of one letter.
const timeout = 30 * time.Second

var (
	// ErrURL is returned for an address of a server that cannot be used.
	ErrURL = errors.New("mail: not the address of an SMTP server")
	// ErrAddress is returned for a recipient that is no e-mail address.
	ErrAddress = errors.New("mail: not an e-mail address")
)

// Sender sends letters through one SMTP server.
type Sender struct {
	// host is the server, with its port.
	host string
	// implicitTLS is a server that speaks TLS from the first byte.
	implicitTLS bool
	user, pass  string
	from        string
	now         func() time.Time
}

// New returns a sender for the server an address names:
// smtp://user:password@host:587?from=reader@example.org, or smtps:// for a
// server that speaks TLS from the start. Without a port it is 587, or 465
// for smtps; without from the letters come from freshgo@ the host. The error
// does not repeat the address: it may carry a password.
func New(address string) (*Sender, error) {
	u, err := url.Parse(address)
	if err != nil || u.Hostname() == "" || u.Scheme != "smtp" && u.Scheme != "smtps" {
		return nil, fmt.Errorf("%w: want smtp[s]://user:password@host:port?from=address", ErrURL)
	}
	s := &Sender{implicitTLS: u.Scheme == "smtps", from: u.Query().Get("from"), now: time.Now}
	port := u.Port()
	if port == "" {
		port = "587"
		if s.implicitTLS {
			port = "465"
		}
	}
	s.host = net.JoinHostPort(u.Hostname(), port)
	if u.User != nil {
		s.user = u.User.Username()
		s.pass, _ = u.User.Password()
	}
	if s.from == "" {
		s.from = "freshgo@" + u.Hostname()
	}
	if _, err := netmail.ParseAddress(s.from); err != nil {
		return nil, fmt.Errorf("%w: sender %q", ErrURL, s.from)
	}
	return s, nil
}

// Send sends a letter of plain text to one recipient.
func (s *Sender) Send(ctx context.Context, to, subject, body string) error {
	recipient, err := netmail.ParseAddress(to)
	if err != nil || strings.ContainsAny(to, "\r\n") {
		return fmt.Errorf("%w: %q", ErrAddress, to)
	}
	sender, _ := netmail.ParseAddress(s.from)
	message, err := s.message(sender.Address, recipient.Address, subject, body)
	if err != nil {
		return err
	}

	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	conn, err := (&net.Dialer{}).DialContext(ctx, "tcp", s.host)
	if err != nil {
		return fmt.Errorf("mail: %w", err)
	}
	defer func() { _ = conn.Close() }()
	if deadline, ok := ctx.Deadline(); ok {
		_ = conn.SetDeadline(deadline)
	}
	name, _, _ := net.SplitHostPort(s.host)
	if s.implicitTLS {
		conn = tls.Client(conn, &tls.Config{ServerName: name, MinVersion: tls.VersionTLS12})
	}
	client, err := smtp.NewClient(conn, name)
	if err != nil {
		return fmt.Errorf("mail: %w", err)
	}
	defer func() { _ = client.Close() }()
	if ok, _ := client.Extension("STARTTLS"); ok && !s.implicitTLS {
		if err := client.StartTLS(&tls.Config{ServerName: name, MinVersion: tls.VersionTLS12}); err != nil {
			return fmt.Errorf("mail: %w", err)
		}
	}
	if s.user != "" {
		// PlainAuth refuses to send the password over a connection that is
		// not encrypted, unless the server is this machine.
		if err := client.Auth(smtp.PlainAuth("", s.user, s.pass, name)); err != nil {
			return fmt.Errorf("mail: %w", err)
		}
	}
	if err := client.Mail(sender.Address); err != nil {
		return fmt.Errorf("mail: %w", err)
	}
	if err := client.Rcpt(recipient.Address); err != nil {
		return fmt.Errorf("mail: %w", err)
	}
	w, err := client.Data()
	if err != nil {
		return fmt.Errorf("mail: %w", err)
	}
	if _, err := w.Write(message); err != nil {
		return fmt.Errorf("mail: %w", err)
	}
	if err := w.Close(); err != nil {
		return fmt.Errorf("mail: %w", err)
	}
	return client.Quit()
}

// message writes a letter: headers, and the text as quoted-printable UTF-8.
func (s *Sender) message(from, to, subject, body string) ([]byte, error) {
	random := make([]byte, 12)
	if _, err := rand.Read(random); err != nil {
		return nil, err
	}
	_, domain, _ := strings.Cut(from, "@")
	var b bytes.Buffer
	for _, header := range [][2]string{
		{"From", from}, {"To", to}, {"Subject", mime.QEncoding.Encode("utf-8", strings.NewReplacer("\r", " ", "\n", " ").Replace(subject))},
		{"Date", s.now().Format(time.RFC1123Z)}, {"Message-ID", "<" + hex.EncodeToString(random) + "@" + domain + ">"},
		{"MIME-Version", "1.0"}, {"Content-Type", "text/plain; charset=utf-8"}, {"Content-Transfer-Encoding", "quoted-printable"},
	} {
		b.WriteString(header[0] + ": " + header[1] + "\r\n")
	}
	b.WriteString("\r\n")
	text := quotedprintable.NewWriter(&b)
	if _, err := text.Write([]byte(strings.ReplaceAll(strings.ReplaceAll(body, "\r\n", "\n"), "\n", "\r\n"))); err != nil {
		return nil, err
	}
	if err := text.Close(); err != nil {
		return nil, err
	}
	return b.Bytes(), nil
}
