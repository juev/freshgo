package main

import (
	"flag"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/juev/freshgo/internal/config"
)

// The pair that vapid-keys prints is what the settings of Web Push take, as
// the lines of a configuration file and as flags, and every run makes a new one.
func TestVapidKeys(t *testing.T) {
	code, stdout, stderr := runCLI(t, "vapid-keys")
	if code != 0 || stderr != "" {
		t.Fatalf("vapid-keys: code %d, stderr %q", code, stderr)
	}
	lines := strings.Split(strings.TrimSpace(stdout), "\n")
	if len(lines) != 2 || !strings.HasPrefix(lines[0], "vapid-public-key: ") || !strings.HasPrefix(lines[1], "vapid-private-key: ") {
		t.Fatalf("output:\n%s\nwant the public and the private key, one to a line", stdout)
	}

	// As a configuration file.
	path := filepath.Join(t.TempDir(), "freshgo.yaml")
	if err := os.WriteFile(path, []byte(stdout+"vapid-subject: mailto:ops@example.org\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	settings, err := config.ReadFile(path)
	if err != nil {
		t.Fatalf("the output is not a configuration file: %v", err)
	}
	fs := flag.NewFlagSet("test", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	conf := config.Bind(fs, func(string) string { return "" })
	settings.Apply(fs)
	if err := fs.Parse(nil); err != nil {
		t.Fatal(err)
	}
	if err := conf.Validate(); err != nil || conf.VAPIDPublicKey == "" || conf.VAPIDPrivateKey == "" {
		t.Errorf("the keys of a configuration file: %+v, %v", conf, err)
	}

	// As flags.
	var args []string
	for _, line := range lines {
		name, value, _ := strings.Cut(line, ": ")
		args = append(args, "-"+name+"="+value)
	}
	fs = flag.NewFlagSet("test", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	conf = config.Bind(fs, func(string) string { return "" })
	if err := fs.Parse(append(args, "-vapid-subject=https://example.org/contact")); err != nil {
		t.Fatal(err)
	}
	if err := conf.Validate(); err != nil {
		t.Errorf("the keys as flags: %v", err)
	}

	if _, again, _ := runCLI(t, "vapid-keys"); again == stdout {
		t.Error("two runs printed the same pair")
	}
	if code, _, stderr := runCLI(t, "vapid-keys", "extra"); code != 1 || !strings.Contains(stderr, `unexpected argument "extra"`) {
		t.Errorf("an argument: code %d, %q", code, stderr)
	}
	if code, out, _ := runCLI(t, "help"); code != 0 || !strings.Contains(out, "vapid-keys") {
		t.Errorf("the list of commands does not name vapid-keys:\n%s", out)
	}
}

// The scripts of completion know the command and the flags of Web Push.
func TestCompletionKnowsWebPush(t *testing.T) {
	_, script, _ := runCLI(t, "completion", "fish")
	for _, want := range []string{"-a vapid-keys", "-o vapid-public-key", "-o vapid-private-key", "-o vapid-subject"} {
		if !strings.Contains(script, want) {
			t.Errorf("the script of fish has no %q", want)
		}
	}
}
