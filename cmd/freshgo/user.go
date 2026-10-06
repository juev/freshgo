package main

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"strings"

	"golang.org/x/crypto/bcrypt"
	"golang.org/x/term"

	"github.com/juev/freshgo/internal/store"
)

// minPasswordLength is the shortest password FreshRSS accepts
// (FreshRSS_password_Util::check).
const minPasswordLength = 7

func userCommands() []command {
	return []command{
		{"create", "add a user; the password is read from standard input", runUserCreate},
		{"passwd", "change the password of a user, read from standard input", runUserPasswd},
		{"list", "list the users", runUserList},
		{"delete", "delete a user with all their feeds and entries", runUserDelete},
	}
}

// synopsis makes the help of a subcommand start with how it is called:
// the flag package knows the flags but not the arguments after them.
func synopsis(fs *flag.FlagSet, e env, line string) {
	fs.Usage = func() {
		_, _ = fmt.Fprintf(e.stderr, "Usage: freshgo %s\n", line)
		fs.PrintDefaults()
	}
}

// operand returns the only argument of a subcommand, called what in errors.
func operand(fs *flag.FlagSet, what string) (string, error) {
	if fs.NArg() != 1 {
		return "", fmt.Errorf("%s is required", what)
	}
	return fs.Arg(0), nil
}

// findUser returns the user a subcommand was told to work on.
func findUser(ctx context.Context, db *store.Store, name string) (*store.User, error) {
	if name == "" {
		return nil, errors.New("a user name is required")
	}
	u, err := db.UserByName(ctx, name)
	if errors.Is(err, store.ErrNotFound) {
		return nil, fmt.Errorf("no user %q", name)
	}
	return u, err
}

// readPassword takes a new password from standard input and returns its
// hash. At a terminal it is asked for twice, without echo; otherwise it is
// the first line of the input.
func readPassword(e env) (string, error) {
	var password string
	if f, ok := e.stdin.(*os.File); ok && term.IsTerminal(int(f.Fd())) {
		ask := func(prompt string) (string, error) {
			_, _ = io.WriteString(e.stderr, prompt)
			typed, err := term.ReadPassword(int(f.Fd()))
			_, _ = io.WriteString(e.stderr, "\n")
			return string(typed), err
		}
		first, err := ask("Password: ")
		if err != nil {
			return "", err
		}
		second, err := ask("Once more: ")
		if err != nil {
			return "", err
		}
		if first != second {
			return "", errors.New("the passwords differ")
		}
		password = first
	} else {
		line, err := bufio.NewReader(e.stdin).ReadString('\n')
		if err != nil && !errors.Is(err, io.EOF) {
			return "", err
		}
		password = strings.TrimRight(line, "\r\n")
	}
	if len(password) < minPasswordLength {
		return "", fmt.Errorf("the password must be at least %d characters long", minPasswordLength)
	}
	hash, err := bcrypt.GenerateFromPassword([]byte(password), bcrypt.DefaultCost)
	if err != nil {
		return "", err
	}
	return string(hash), nil
}

func runUserCreate(ctx context.Context, e env, args []string) (err error) {
	fs, conf := newFlagSet(e, "user create")
	synopsis(fs, e, "user create [flags] <name>")
	admin := fs.Bool("admin", false, "make the user an administrator; the first user of an installation is one anyway")
	db, err := openStore(ctx, fs, conf, args, 1)
	if err != nil {
		return err
	}
	defer func() { err = errors.Join(err, db.Close()) }()
	name, err := operand(fs, "a user name")
	if err != nil {
		return err
	}
	if !store.ValidUserName(name) {
		return fmt.Errorf("%q cannot be a user name: up to 39 Latin letters, digits and _ . @ -, starting with a letter, a digit or _", name)
	}
	// Before the password is asked for: nobody wants to type it twice to
	// learn that the name is taken.
	if _, err := db.UserByName(ctx, name); err == nil {
		return fmt.Errorf("user %q already exists", name)
	} else if !errors.Is(err, store.ErrNotFound) {
		return err
	}
	hash, err := readPassword(e)
	if err != nil {
		return err
	}
	// One password for the web interface and for API clients; each can be
	// changed on its own in the interface.
	settings := map[string]any{"passwordHash": hash}
	if *admin {
		settings["is_admin"] = true
	}
	raw, err := json.Marshal(settings)
	if err != nil {
		return err
	}
	if err := db.CreateUser(ctx, &store.User{Name: name, APIPasswordHash: hash, Settings: raw}); err != nil {
		return err
	}
	_, err = fmt.Fprintf(e.stdout, "user %s created\n", name)
	return err
}

func runUserPasswd(ctx context.Context, e env, args []string) (err error) {
	fs, conf := newFlagSet(e, "user passwd")
	synopsis(fs, e, "user passwd [flags] <name>")
	db, err := openStore(ctx, fs, conf, args, 1)
	if err != nil {
		return err
	}
	defer func() { err = errors.Join(err, db.Close()) }()
	name, err := operand(fs, "a user name")
	if err != nil {
		return err
	}
	u, err := findUser(ctx, db, name)
	if err != nil {
		return err
	}
	hash, err := readPassword(e)
	if err != nil {
		return err
	}
	err = db.InTx(ctx, func(tx *store.Store) error {
		err := tx.UpdateUserSettings(ctx, u.ID, func(settings map[string]json.RawMessage) error {
			raw, err := json.Marshal(hash)
			settings["passwordHash"] = raw
			return err
		})
		if err != nil {
			return err
		}
		if err := tx.SetAPIPasswordHash(ctx, u.ID, hash); err != nil {
			return err
		}
		// Whoever is logged in with the old password is logged out.
		return tx.DeleteUserSessions(ctx, u.ID, "")
	})
	if err != nil {
		return err
	}
	_, err = fmt.Fprintf(e.stdout, "password of %s changed; browsers and clients have to log in again\n", name)
	return err
}

func runUserList(ctx context.Context, e env, args []string) (err error) {
	fs, conf := newFlagSet(e, "user list")
	db, err := openStore(ctx, fs, conf, args, 0)
	if err != nil {
		return err
	}
	defer func() { err = errors.Join(err, db.Close()) }()
	users, err := db.Users(ctx)
	if err != nil {
		return err
	}
	for _, u := range users {
		feeds, err := db.Feeds(ctx, u.ID)
		if err != nil {
			return err
		}
		entries, err := db.CountEntries(ctx, u.ID)
		if err != nil {
			return err
		}
		line := fmt.Sprintf("%s: %d feeds, %d entries", u.Name, len(feeds), entries)
		if u.APIPasswordHash == "" {
			line += ", no API password"
		}
		if _, err := fmt.Fprintln(e.stdout, line); err != nil {
			return err
		}
	}
	return nil
}

func runUserDelete(ctx context.Context, e env, args []string) (err error) {
	fs, conf := newFlagSet(e, "user delete")
	synopsis(fs, e, "user delete [flags] <name>")
	db, err := openStore(ctx, fs, conf, args, 1)
	if err != nil {
		return err
	}
	defer func() { err = errors.Join(err, db.Close()) }()
	name, err := operand(fs, "a user name")
	if err != nil {
		return err
	}
	u, err := findUser(ctx, db, name)
	if err != nil {
		return err
	}
	if err := db.DeleteUser(ctx, u.ID); err != nil {
		return err
	}
	_, err = fmt.Fprintf(e.stdout, "user %s deleted\n", name)
	return err
}
