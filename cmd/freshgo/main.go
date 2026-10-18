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

	"github.com/juev/freshgo/internal/config"
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
	// settings are those of the configuration file named by -config.
	settings config.Settings
}

type command struct {
	name    string
	summary string
	run     func(ctx context.Context, e env, args []string) error
	// actions are those of a command that only hands its arguments over to
	// one of them, which has no run of its own.
	actions []command
}

func commands() []command {
	return []command{
		{"serve", "run the HTTP server and the refresh scheduler", runServe, nil},
		{"refresh", "refresh feeds once and exit", runRefresh, nil},
		{"purge", "delete old entries according to the archiving settings", runPurge, nil},
		{"import", "import a FreshRSS installation into an empty database, or a Miniflux account into a user", runImport, nil},
		{"backup", "copy the database to a file while the server runs", runBackup, nil},
		{"restore", "check a copy made by backup before it is put in place", runRestore, nil},
		{"user", "manage users: create, passwd, list, delete", nil, userCommands()},
		{"feed", "manage feeds: add", nil, feedCommands()},
		{"opml", "import or export subscriptions as OPML", nil, opmlCommands()},
		{"doctor", "check the configuration, the database and the services it names, changing nothing", runDoctor, nil},
		{"healthcheck", "ask the running server whether it works, for the health check of a container", runHealthcheck, nil},
		{"vapid-keys", "print a new pair of keys for Web Push", runVapidKeys, nil},
		{"completion", "print a script that completes the commands and their flags in bash, zsh or fish", runCompletion, nil},
		{"version", "print the version", runVersion, nil},
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
	global := flag.NewFlagSet("freshgo", flag.ContinueOnError)
	global.SetOutput(io.Discard)
	file := global.String("config", "", "")
	switch err := global.Parse(args); {
	case errors.Is(err, flag.ErrHelp):
		usage(e.stdout)
		return 0
	case err != nil:
		_, _ = fmt.Fprintf(e.stderr, "freshgo: %v\n\n", err)
		usage(e.stderr)
		return 2
	}
	args = global.Args()
	if *file != "" {
		settings, err := config.ReadFile(*file)
		if err != nil {
			_, _ = fmt.Fprintf(e.stderr, "freshgo: %v\n", err)
			return 1
		}
		e.settings = settings
	}
	if len(args) == 0 {
		usage(e.stderr)
		return 2
	}
	if args[0] == "help" {
		usage(e.stdout)
		return 0
	}
	for _, c := range commands() {
		if c.name != args[0] {
			continue
		}
		run := c.run
		if c.actions != nil {
			run = group(c.name, c.actions)
		}
		err := run(ctx, e, args[1:])
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
	b.WriteString("Usage: freshgo [-config file] <command> [flags]\n\nCommands:\n")
	for _, c := range commands() {
		fmt.Fprintf(&b, "  %-11s %s\n", c.name, c.summary)
	}
	b.WriteString("\nRun \"freshgo <command> -h\" for the flags of a command. A flag of the settings\n" +
		"can be set in the YAML file -config names as well, by its name without the dash:\n" +
		"a flag wins over the file, the file over the environment.\n")
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
