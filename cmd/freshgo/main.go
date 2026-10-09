// Command freshgo is a FreshRSS-compatible feed aggregator server.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"runtime/debug"
	"strings"
	"syscall"
)

// version is set at build time with -ldflags "-X main.version=...".
var version = ""

// env is what a subcommand gets from the process, passed explicitly so that
// commands can be run from tests.
type env struct {
	stdin  io.Reader
	stdout io.Writer
	stderr io.Writer
	getenv func(string) string
}

type command struct {
	name    string
	summary string
	run     func(ctx context.Context, e env, args []string) error
}

func commands() []command {
	return []command{
		{"serve", "run the HTTP server and the refresh scheduler", runServe},
		{"refresh", "refresh feeds once and exit", runRefresh},
		{"purge", "delete old entries according to the archiving settings", runPurge},
		{"import", "import a FreshRSS installation into an empty database", runImport},
		{"user", "manage users: create, passwd, list, delete", group("user", userCommands())},
		{"feed", "manage feeds: add", group("feed", feedCommands())},
		{"opml", "import or export subscriptions as OPML", group("opml", opmlCommands())},
		{"version", "print the version", runVersion},
	}
}

// gcPercent is how far the heap may grow over what is in use before the
// garbage collector runs, in percent. The runtime of Go starts at 100. What
// stays in use here is a few megabytes and an answer of the API allocates
// more than one, so at 100 a collection ran every few requests: at 200 an
// answer takes a third less time, for 8 MB more of heap.
const gcPercent = 200

// tuneGC sets gcPercent unless the environment says how the collector is
// to run.
func tuneGC(getenv func(string) string) {
	if getenv("GOGC") == "" {
		debug.SetGCPercent(gcPercent)
	}
}

func main() {
	tuneGC(os.Getenv)
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	code := run(ctx, env{stdin: os.Stdin, stdout: os.Stdout, stderr: os.Stderr, getenv: os.Getenv}, os.Args[1:])
	stop()
	os.Exit(code)
}

func run(ctx context.Context, e env, args []string) int {
	if len(args) == 0 {
		usage(e.stderr)
		return 2
	}
	switch args[0] {
	case "help", "-h", "-help", "--help":
		usage(e.stdout)
		return 0
	}
	for _, c := range commands() {
		if c.name != args[0] {
			continue
		}
		err := c.run(ctx, e, args[1:])
		switch {
		case err == nil:
			return 0
		case errors.Is(err, flag.ErrHelp):
			return 0
		default:
			_, _ = fmt.Fprintf(e.stderr, "freshgo %s: %v\n", c.name, err)
			return 1
		}
	}
	_, _ = fmt.Fprintf(e.stderr, "freshgo: unknown command %q\n\n", args[0])
	usage(e.stderr)
	return 2
}

// usage prints the command list. Its output goes to a terminal stream, where
// a failed write has nowhere to be reported.
func usage(w io.Writer) {
	var b strings.Builder
	b.WriteString("Usage: freshgo <command> [flags]\n\nCommands:\n")
	for _, c := range commands() {
		fmt.Fprintf(&b, "  %-8s %s\n", c.name, c.summary)
	}
	b.WriteString("\nRun \"freshgo <command> -h\" for the flags of a command.\n")
	_, _ = io.WriteString(w, b.String())
}

// group returns the command that hands its arguments over to the one of
// actions named first among them.
func group(name string, actions []command) func(context.Context, env, []string) error {
	return func(ctx context.Context, e env, args []string) error {
		var b strings.Builder
		fmt.Fprintf(&b, "Usage: freshgo %s <action> [flags]\n\nActions:\n", name)
		for _, a := range actions {
			fmt.Fprintf(&b, "  %-8s %s\n", a.name, a.summary)
			if len(args) > 0 && a.name == args[0] {
				return a.run(ctx, e, args[1:])
			}
		}
		fmt.Fprintf(&b, "\nRun \"freshgo %s <action> -h\" for the flags of an action.\n", name)
		if len(args) > 0 {
			switch args[0] {
			case "help", "-h", "-help", "--help":
				_, err := io.WriteString(e.stdout, b.String())
				return err
			}
			return fmt.Errorf("unknown action %q\n\n%s", args[0], strings.TrimRight(b.String(), "\n"))
		}
		return fmt.Errorf("no action given\n\n%s", strings.TrimRight(b.String(), "\n"))
	}
}

func runVersion(_ context.Context, e env, _ []string) error {
	_, err := fmt.Fprintln(e.stdout, "freshgo", buildVersion())
	return err
}

func buildVersion() string {
	if version != "" {
		return version
	}
	if info, ok := debug.ReadBuildInfo(); ok && info.Main.Version != "" {
		return info.Main.Version
	}
	return "(devel)"
}
