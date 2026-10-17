package main

import (
	"bytes"
	"context"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"runtime/debug"
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
	path := filepath.Join(t.TempDir(), "freshgo.sqlite")
	database := "sqlite://" + path
	if code, _, stderr := runCLI(t, "import", "-database-url", database); code != 1 || !strings.Contains(stderr, "-data is required") {
		t.Errorf("import without -data: code %d, stderr %q", code, stderr)
	}
	for _, data := range []string{filepath.Join(t.TempDir(), "missing"), t.TempDir(), "main_test.go"} {
		if code, _, stderr := runCLI(t, "import", "-database-url", database, "-data", data); code != 1 || !strings.Contains(stderr, "not a FreshRSS data directory") {
			t.Errorf("import from %s: code %d, stderr %q", data, code, stderr)
		}
	}
	// The database is checked before it is made.
	if _, err := os.Stat(path); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("a refused import left %s behind: %v", path, err)
	}
	if code, _, stderr := runCLI(t, "import", "-database-url", "mysql://x", "-data", "."); code != 1 || !strings.Contains(stderr, "unknown scheme") {
		t.Errorf("import into MySQL: code %d, stderr %q", code, stderr)
	}
	if code, stdout, stderr := runCLI(t, "import", "-h"); code != 0 || stdout != "" || !strings.Contains(stderr, "-source-database-url") {
		t.Errorf("import -h: code %d, stdout %q, stderr %q", code, stdout, stderr)
	}
}

// -config names a file with the settings of every command, which a flag
// overrides.
func TestConfigFile(t *testing.T) {
	dir := t.TempDir()
	databases := map[string]string{}
	for _, name := range []string{"alice", "bob"} {
		databases[name] = "sqlite://" + filepath.Join(dir, name+".sqlite")
		if code, _, stderr := runCLIInput(t, "correct horse\n", "user", "create", "-database-url", databases[name], name); code != 0 {
			t.Fatalf("user create %s: %s", name, stderr)
		}
	}
	file := filepath.Join(dir, "freshgo.yaml")
	if err := os.WriteFile(file, []byte("database-url: "+databases["alice"]+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if code, stdout, stderr := runCLI(t, "-config", file, "user", "list"); code != 0 || !strings.HasPrefix(stdout, "alice:") {
		t.Errorf("-config: code %d, stdout %q, stderr %q", code, stdout, stderr)
	}
	if code, stdout, stderr := runCLI(t, "-config="+file, "user", "list", "-database-url", databases["bob"]); code != 0 || !strings.HasPrefix(stdout, "bob:") {
		t.Errorf("-config and a flag: code %d, stdout %q, stderr %q", code, stdout, stderr)
	}

	if err := os.WriteFile(file, []byte("database: x\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if code, _, stderr := runCLI(t, "-config", file, "user", "list"); code != 1 || !strings.Contains(stderr, `unknown setting "database"`) {
		t.Errorf("a file with an unknown setting: code %d, stderr %q", code, stderr)
	}
	if code, _, stderr := runCLI(t, "-config"); code != 2 || !strings.Contains(stderr, "flag needs an argument") {
		t.Errorf("-config without a file: code %d, stderr %q", code, stderr)
	}
	if code, stdout, _ := runCLI(t, "-h"); code != 0 || !strings.Contains(stdout, "[-config file]") {
		t.Errorf("-h: code %d, stdout %q", code, stdout)
	}
}

// The collector runs as the server sets it, unless the environment says
// how it is to run.
func TestTuneGC(t *testing.T) {
	before := debug.SetGCPercent(100)
	t.Cleanup(func() { debug.SetGCPercent(before) })

	tuneGC(func(string) string { return "50" })
	if got := debug.SetGCPercent(100); got != 100 {
		t.Errorf("with GOGC set: %d, want it left at 100", got)
	}
	tuneGC(func(string) string { return "" })
	if got := debug.SetGCPercent(100); got != gcPercent {
		t.Errorf("without GOGC: %d, want %d", got, gcPercent)
	}
}
