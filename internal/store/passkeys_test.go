package store

import (
	"bytes"
	"context"
	"errors"
	"reflect"
	"testing"
)

// A passkey is named by its credential ID: it is stored once, belongs to the
// user who registered it, is found from the credential alone, and only its
// user renames or removes it.
func TestPasskeys(t *testing.T) {
	eachEngine(t, func(t *testing.T, s *Store) {
		ctx := context.Background()
		alice, bob := mustUser(t, s, "alice"), mustUser(t, s, "bob")
		// Bytes a credential ID can hold, a zero among them.
		first := &Passkey{UserID: alice.ID, CredentialID: []byte{0, 1, 2, 255}, Name: "laptop", Credential: []byte(`{"a":1}`), Created: 10}
		second := &Passkey{UserID: alice.ID, CredentialID: []byte{9, 9}, Name: "phone", Credential: []byte(`{"a":2}`), Created: 20}
		for _, p := range []*Passkey{first, second} {
			if err := s.AddPasskey(ctx, p); err != nil || p.ID == 0 {
				t.Fatalf("AddPasskey(%q) = %v, id %d", p.Name, err, p.ID)
			}
		}
		got, err := s.Passkeys(ctx, alice.ID)
		if err != nil || !reflect.DeepEqual(got, []*Passkey{first, second}) {
			t.Fatalf("Passkeys(alice) = %+v, %v; want her two, the oldest first", got, err)
		}
		if got, err := s.Passkeys(ctx, bob.ID); err != nil || len(got) != 0 {
			t.Errorf("Passkeys(bob) = %+v, %v; want none", got, err)
		}

		// A credential belongs to one user, whoever asks to store it.
		stolen := &Passkey{UserID: bob.ID, CredentialID: first.CredentialID, Name: "mine now", Credential: []byte(`{}`), Created: 30}
		if err := s.AddPasskey(ctx, stolen); !errors.Is(err, ErrConflict) {
			t.Errorf("AddPasskey of a credential alice has = %v, want ErrConflict", err)
		}
		if got, _ := s.Passkeys(ctx, bob.ID); len(got) != 0 {
			t.Errorf("bob got alice's credential: %+v", got)
		}

		found, err := s.PasskeyByCredential(ctx, second.CredentialID)
		if err != nil || found.UserID != alice.ID || found.Name != "phone" || !bytes.Equal(found.Credential, second.Credential) {
			t.Errorf("PasskeyByCredential = %+v, %v; want alice's phone", found, err)
		}
		if _, err := s.PasskeyByCredential(ctx, []byte{7}); !errors.Is(err, ErrNotFound) {
			t.Errorf("PasskeyByCredential of an unknown credential = %v, want ErrNotFound", err)
		}

		// A login keeps the credential the library updated, and when.
		if err := s.UsePasskey(ctx, first.ID, []byte(`{"a":3}`), 40); err != nil {
			t.Fatal(err)
		}
		if found, _ := s.PasskeyByCredential(ctx, first.CredentialID); string(found.Credential) != `{"a":3}` || found.Used != 40 || found.Name != "laptop" {
			t.Errorf("after a login: %+v", found)
		}
		if err := s.UsePasskey(ctx, 9999, nil, 1); !errors.Is(err, ErrNotFound) {
			t.Errorf("UsePasskey of a passkey that is not there = %v, want ErrNotFound", err)
		}

		// Only the user renames or removes a passkey.
		if err := s.RenamePasskey(ctx, bob.ID, first.ID, "hijacked"); !errors.Is(err, ErrNotFound) {
			t.Errorf("RenamePasskey by another user = %v, want ErrNotFound", err)
		}
		if err := s.DeletePasskey(ctx, bob.ID, first.ID); !errors.Is(err, ErrNotFound) {
			t.Errorf("DeletePasskey by another user = %v, want ErrNotFound", err)
		}
		if err := s.RenamePasskey(ctx, alice.ID, first.ID, "desk"); err != nil {
			t.Fatal(err)
		}
		if found, _ := s.PasskeyByCredential(ctx, first.CredentialID); found.Name != "desk" {
			t.Errorf("name after renaming = %q, want desk", found.Name)
		}
		if err := s.DeletePasskey(ctx, alice.ID, first.ID); err != nil {
			t.Fatal(err)
		}
		if _, err := s.PasskeyByCredential(ctx, first.CredentialID); !errors.Is(err, ErrNotFound) {
			t.Errorf("a removed passkey is still found: %v", err)
		}
		if err := s.DeletePasskey(ctx, alice.ID, first.ID); !errors.Is(err, ErrNotFound) {
			t.Errorf("DeletePasskey twice = %v, want ErrNotFound", err)
		}
		// The credential of a removed passkey can be registered again.
		first.ID = 0
		if err := s.AddPasskey(ctx, first); err != nil {
			t.Errorf("AddPasskey after its removal: %v", err)
		}

		// All of a user's go at once, and with the user.
		if err := s.DeleteUserPasskeys(ctx, alice.ID); err != nil {
			t.Fatal(err)
		}
		if got, _ := s.Passkeys(ctx, alice.ID); len(got) != 0 {
			t.Errorf("passkeys left: %+v", got)
		}
		mine := &Passkey{UserID: bob.ID, CredentialID: []byte{5}, Name: "key", Credential: []byte(`{}`), Created: 1}
		if err := s.AddPasskey(ctx, mine); err != nil {
			t.Fatal(err)
		}
		if err := s.DeleteUser(ctx, bob.ID); err != nil {
			t.Fatal(err)
		}
		var n int
		if err := s.queryRow(ctx, `SELECT COUNT(*) FROM passkeys`).Scan(&n); err != nil || n != 0 {
			t.Errorf("rows left after the user was deleted: %d, %v", n, err)
		}
	})
}
