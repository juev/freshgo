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

var errNotImplemented = errors.New("not implemented yet")

// env is what a subcommand gets from the process, passed explicitly so that
// commands can be run from tests.
type env struct {
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
		{"serve", "run the HTTP server and the refresh scheduler", stub},
		{"refresh", "refresh feeds once and exit", stub},
		{"purge", "delete old entries according to the archiving settings", stub},
		{"import", "import a FreshRSS installation into an empty database", stub},
		{"user", "manage users: create, passwd, list, delete", stub},
		{"feed", "manage feeds: add", stub},
		{"opml", "import or export subscriptions as OPML", stub},
		{"version", "print the version", runVersion},
	}
}

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	code := run(ctx, env{stdout: os.Stdout, stderr: os.Stderr, getenv: os.Getenv}, os.Args[1:])
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

func stub(context.Context, env, []string) error {
	return errNotImplemented
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
