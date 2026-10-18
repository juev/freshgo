package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// The script of bash completes the commands, the actions of a command and
// the flags of either, past -config and its file.
func TestCompletionBash(t *testing.T) {
	bash, err := exec.LookPath("bash")
	if err != nil {
		t.Skip("no bash")
	}
	code, script, stderr := runCLI(t, "completion", "bash")
	if code != 0 {
		t.Fatalf("completion bash: code %d, %s", code, stderr)
	}
	path := filepath.Join(t.TempDir(), "freshgo.bash")
	if err := os.WriteFile(path, []byte(script), 0o600); err != nil {
		t.Fatal(err)
	}
	for line, want := range map[string]string{
		"freshgo se":                      "serve",
		"freshgo -config x.yaml us":       "user",
		"freshgo user c":                  "create",
		"freshgo user create -ad":         "-admin",
		"freshgo opml import -us":         "-user",
		"freshgo import -da":              "-data -database-url",
		"freshgo import -so":              "-source-database-url -source-token -source-url",
		"freshgo import -fr":              "-from",
		"freshgo serve -listen :80 -data": "-database-url",
		"freshgo comp":                    "completion",
	} {
		probe := `source "$1"; COMP_WORDS=($2); COMP_CWORD=$((${#COMP_WORDS[@]}-1)); _freshgo; echo "${COMPREPLY[*]}"`
		out, err := exec.Command(bash, "-c", probe, "bash", path, line).CombinedOutput()
		if got := strings.TrimSpace(string(out)); err != nil || got != want {
			t.Errorf("%q: %q, %v; want %q", line, got, err, want)
		}
	}
}

func TestCompletionScripts(t *testing.T) {
	for shell, want := range map[string][]string{
		"zsh": {"#compdef freshgo", "compdef _freshgo freshgo", `'user create') described=(`, `'-admin:make the user an administrator`},
		"fish": {
			"complete -c freshgo -n '__fish_use_subcommand' -f -a serve -d 'run the HTTP server and the refresh scheduler'",
			"complete -c freshgo -n '__fish_seen_subcommand_from opml; and __fish_seen_subcommand_from import' -o user ",
			"complete -c freshgo -n '__fish_seen_subcommand_from import; and not __fish_seen_subcommand_from user feed opml' -o data ",
		},
	} {
		code, script, stderr := runCLI(t, "completion", shell)
		if code != 0 {
			t.Fatalf("completion %s: code %d, %s", shell, code, stderr)
		}
		for _, w := range want {
			if !strings.Contains(script, w) {
				t.Errorf("completion %s has no %q:\n%s", shell, w, script)
			}
		}
	}
	if code, _, stderr := runCLI(t, "completion", "tcsh"); code != 1 || !strings.Contains(stderr, `unknown shell "tcsh"`) {
		t.Errorf("completion tcsh: code %d, %q", code, stderr)
	}
	if code, _, stderr := runCLI(t, "completion"); code != 1 || !strings.Contains(stderr, "bash, zsh or fish") {
		t.Errorf("completion without a shell: code %d, %q", code, stderr)
	}
}
