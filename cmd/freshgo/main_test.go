package main

import (
	"bytes"
	"context"
	"strings"
	"testing"
)

func runCLI(t *testing.T, args ...string) (code int, stdout, stderr string) {
	t.Helper()
	var out, errOut bytes.Buffer
	code = run(context.Background(), env{stdout: &out, stderr: &errOut, getenv: func(string) string { return "" }}, args)
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
