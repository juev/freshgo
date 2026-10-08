package mail_test

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/juev/freshgo/internal/mail"
	"github.com/juev/freshgo/internal/mail/mailtest"
)

// A letter arrives with its subject and text as they were written.
func TestSend(t *testing.T) {
	server := mailtest.New(t)
	sender, err := mail.New(server.URL)
	if err != nil {
		t.Fatal(err)
	}
	body := "Здравствуйте!\n\nСсылка: https://reader.example/validate-email?user=alice&token=abc\n.\nA line that is long enough to be folded by the encoding of the letter, so that what arrives is what was sent."
	if err := sender.Send(context.Background(), "Alice <alice@example.org>", "Подтвердите адрес — freshgo", body); err != nil {
		t.Fatalf("Send: %v", err)
	}
	letters := server.Letters()
	if len(letters) != 1 {
		t.Fatalf("%d letters arrived, want 1", len(letters))
	}
	if got := letters[0]; got.From != "reader@freshgo.test" || got.To != "alice@example.org" || got.Subject != "Подтвердите адрес — freshgo" || got.Body != body {
		t.Errorf("letter = %+v", got)
	}
	// A recipient cannot bring headers of their own.
	for _, to := range []string{"", "nobody", "a@example.org\r\nBcc: b@example.org", "a@example.org, b@example.org"} {
		if err := sender.Send(context.Background(), to, "s", "b"); !errors.Is(err, mail.ErrAddress) {
			t.Errorf("Send to %q: error = %v, want ErrAddress", to, err)
		}
	}
	if len(server.Letters()) != 1 {
		t.Error("a letter went out to an address that is none")
	}
}

func TestNew(t *testing.T) {
	for _, address := range []string{"", "http://mail.example", "smtp://", "smtp://mail.example?from=nobody", "mail.example:25", "smtp//user:secret@mail.example", "stmp://user:secret@mail.example"} {
		_, err := mail.New(address)
		if !errors.Is(err, mail.ErrURL) {
			t.Errorf("New(%q): error = %v, want ErrURL", address, err)
		} else if strings.Contains(err.Error(), "secret") {
			t.Errorf("New(%q) shows the password: %v", address, err)
		}
	}
	for _, address := range []string{"smtp://mail.example", "smtps://user:pass@mail.example:465?from=me@example.org", "smtp://127.0.0.1:2525"} {
		if _, err := mail.New(address); err != nil {
			t.Errorf("New(%q): %v", address, err)
		}
	}
	// A server that is not there is an error, not a wait.
	sender, _ := mail.New("smtp://127.0.0.1:1")
	if err := sender.Send(context.Background(), "a@example.org", "s", "b"); err == nil || strings.Contains(err.Error(), "ErrAddress") {
		t.Errorf("Send to a server that is not there: %v", err)
	}
}
