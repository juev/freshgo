package main

import (
	"bytes"
	"context"
	"path/filepath"
	"strings"
	"testing"
)

func runCLI(t *testing.T, args ...string) (code int, stdout, stderr string) {
	t.Helper()
	return runCLIInput(t, "", args...)
}

// runCLIInput runs a command with input on its standard input.
func runCLIInput(t *testing.T, input string, args ...string) (code int, stdout, stderr string) {
	t.Helper()
	var out, errOut bytes.Buffer
	e := env{stdin: strings.NewReader(input), stdout: &out, stderr: &errOut, getenv: func(string) string { return "" }}
	code = run(context.Background(), e, args)
	return code, out.String(), errOut.String()
}

func TestVersion(t *testing.T) {
	version = "1.2.3"
	t.Cleanup(func() { version = "" })

	code, stdout, _ := runCLI(t, "version")
	if code != 0 || stdout != "freshgo 1.2.3\n" {
		t.Errorf("version: code %d, stdout %q; want 0, %q", code, stdout, "freshgo 1.2.3\n")
	}
}

func TestUnknownCommand(t *testing.T) {
	code, stdout, stderr := runCLI(t, "frobnicate")
	if code != 2 {
		t.Errorf("code = %d, want 2", code)
	}
	if stdout != "" {
		t.Errorf("stdout = %q, want empty", stdout)
	}
	if !strings.Contains(stderr, `unknown command "frobnicate"`) || !strings.Contains(stderr, "Commands:") {
		t.Errorf("stderr = %q, want the error and the command list", stderr)
	}
}

func TestNoArguments(t *testing.T) {
	code, _, stderr := runCLI(t)
	if code != 2 || !strings.Contains(stderr, "Usage: freshgo") {
		t.Errorf("code %d, stderr %q; want 2 and usage", code, stderr)
	}
}

func TestHelpListsEveryCommand(t *testing.T) {
	code, stdout, _ := runCLI(t, "help")
	if code != 0 {
		t.Fatalf("code = %d, want 0", code)
	}
	for _, c := range commands() {
		if !strings.Contains(stdout, "  "+c.name+" ") {
			t.Errorf("help does not list %q", c.name)
		}
	}
}

func TestImport(t *testing.T) {
	database := "sqlite://" + filepath.Join(t.TempDir(), "freshgo.sqlite")
	args := []string{"import", "-database-url", database, "-data", "../../testdata/reference/sqlite/data"}

	code, stdout, stderr := runCLI(t, args...)
	if code != 0 {
		t.Fatalf("import: code %d, stderr %q", code, stderr)
	}
	want := "alice: 3 categories, 8 feeds, 22 entries, 2 labels on 3 entries, 1 custom icons\n" +
		"bob: 2 categories, 3 feeds, 11 entries, 1 labels on 1 entries, 0 custom icons\n"
	if stdout != want || stderr != "" {
		t.Errorf("import:\nstdout %q\n  want %q\nstderr %q", stdout, want, stderr)
	}

	code, stdout, stderr = runCLI(t, args...)
	if code != 1 || stdout != "" || !strings.Contains(stderr, "the database is not empty") {
		t.Errorf("second import: code %d, stdout %q, stderr %q; want 1 and a refusal", code, stdout, stderr)
	}
}

func TestImportUsage(t *testing.T) {
	database := "sqlite://" + filepath.Join(t.TempDir(), "freshgo.sqlite")
	if code, _, stderr := runCLI(t, "import", "-database-url", database); code != 1 || !strings.Contains(stderr, "-data is required") {
		t.Errorf("import without -data: code %d, stderr %q", code, stderr)
	}
	if code, _, stderr := runCLI(t, "import", "-database-url", "mysql://x", "-data", "."); code != 1 || !strings.Contains(stderr, "unknown scheme") {
		t.Errorf("import into MySQL: code %d, stderr %q", code, stderr)
	}
	if code, stdout, stderr := runCLI(t, "import", "-h"); code != 0 || stdout != "" || !strings.Contains(stderr, "-source-database-url") {
		t.Errorf("import -h: code %d, stdout %q, stderr %q", code, stdout, stderr)
	}
}
