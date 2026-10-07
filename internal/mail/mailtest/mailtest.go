// Package mailtest is an SMTP server for tests: it takes every letter and
// keeps it for the test to read.
package mailtest

import (
	"bufio"
	"io"
	"mime"
	"mime/quotedprintable"
	"net"
	netmail "net/mail"
	"strings"
	"sync"
	"testing"
)

// Letter is a letter the server took.
type Letter struct {
	From, To string
	Subject  string
	Body     string
}

// Server is an SMTP server on this machine.
type Server struct {
	// URL is the address to give mail.New.
	URL string

	mu      sync.Mutex
	letters []Letter
}

// Letters returns the letters taken so far.
func (s *Server) Letters() []Letter {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]Letter{}, s.letters...)
}

// New starts a server that lives as long as the test.
func New(t testing.TB) *Server {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = listener.Close() })
	s := &Server{URL: "smtp://" + listener.Addr().String() + "?from=reader@freshgo.test"}
	go func() {
		for {
			conn, err := listener.Accept()
			if err != nil {
				return
			}
			go s.serve(conn)
		}
	}()
	return s
}

func (s *Server) serve(conn net.Conn) {
	defer func() { _ = conn.Close() }()
	r := bufio.NewReader(conn)
	say := func(line string) { _, _ = io.WriteString(conn, line+"\r\n") }
	say("220 mailtest")
	var letter Letter
	for {
		line, err := r.ReadString('\n')
		if err != nil {
			return
		}
		command := strings.ToUpper(strings.TrimSpace(line))
		switch {
		case strings.HasPrefix(command, "EHLO"), strings.HasPrefix(command, "HELO"):
			say("250 mailtest")
		case strings.HasPrefix(command, "MAIL FROM:"):
			letter = Letter{From: strings.Trim(strings.TrimSpace(line)[len("MAIL FROM:"):], "<>")}
			say("250 ok")
		case strings.HasPrefix(command, "RCPT TO:"):
			letter.To = strings.Trim(strings.TrimSpace(line)[len("RCPT TO:"):], "<>")
			say("250 ok")
		case command == "DATA":
			say("354 go on")
			var data strings.Builder
			for {
				line, err := r.ReadString('\n')
				if err != nil {
					return
				}
				if line == ".\r\n" {
					break
				}
				data.WriteString(strings.TrimPrefix(line, "."))
			}
			if message, err := netmail.ReadMessage(strings.NewReader(data.String())); err == nil {
				letter.Subject, _ = new(mime.WordDecoder).DecodeHeader(message.Header.Get("Subject"))
				body, _ := io.ReadAll(quotedprintable.NewReader(message.Body))
				// The line break that ends the data is not of the letter.
				letter.Body = strings.TrimSuffix(strings.ReplaceAll(string(body), "\r\n", "\n"), "\n")
			}
			s.mu.Lock()
			s.letters = append(s.letters, letter)
			s.mu.Unlock()
			say("250 taken")
		case command == "QUIT":
			say("221 bye")
			return
		default:
			say("250 ok")
		}
	}
}
