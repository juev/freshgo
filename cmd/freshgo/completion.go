package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"regexp"
	"strings"
)

// completed is what a script of completion knows of a command: its words
// after "freshgo", its summary, and its actions or its flags.
type completed struct {
	path    string
	summary string
	actions []completed
	flags   []completedFlag
}

type completedFlag struct{ name, usage string }

// helpLine is the line of the help of a flag that names it; the line after
// it holds the usage.
var helpLine = regexp.MustCompile(`^  -([A-Za-z0-9][-A-Za-z0-9]*)`)

// flagsOf asks a command for its help and reads the flags out of it: the
// commands make their flag sets as they run, and every one of them parses
// its arguments before it does anything else.
func flagsOf(ctx context.Context, c command) []completedFlag {
	var help strings.Builder
	e := env{stdin: strings.NewReader(""), stdout: io.Discard, stderr: &help, getenv: func(string) string { return "" }}
	_ = c.run(ctx, e, []string{"-h"})
	var flags []completedFlag
	lines := strings.Split(help.String(), "\n")
	for i, line := range lines {
		m := helpLine.FindStringSubmatch(line)
		if m == nil {
			continue
		}
		f := completedFlag{name: m[1]}
		if i+1 < len(lines) {
			f.usage = strings.TrimSpace(lines[i+1])
			if cut := strings.Index(f.usage, " (default "); cut >= 0 {
				f.usage = f.usage[:cut]
			}
		}
		flags = append(flags, f)
	}
	return flags
}

func completions(ctx context.Context) []completed {
	var all []completed
	for _, c := range commands() {
		if c.name == "completion" {
			all = append(all, completed{path: c.name, summary: c.summary})
			continue
		}
		one := completed{path: c.name, summary: c.summary}
		for _, a := range c.actions {
			one.actions = append(one.actions, completed{path: c.name + " " + a.name, summary: a.summary, flags: flagsOf(ctx, a)})
		}
		if c.actions == nil {
			one.flags = flagsOf(ctx, c)
		}
		all = append(all, one)
	}
	return all
}

func runCompletion(ctx context.Context, e env, args []string) error {
	fs := flag.NewFlagSet("freshgo completion", flag.ContinueOnError)
	fs.SetOutput(e.stderr)
	fs.Usage = func() {
		_, _ = fmt.Fprintln(fs.Output(), "Usage: freshgo completion bash|zsh|fish\n\n"+
			"Prints a script that completes the commands of freshgo and their flags. Load it in the shell:\n"+
			"  bash: source <(freshgo completion bash)\n"+
			"  zsh:  source <(freshgo completion zsh)\n"+
			"  fish: freshgo completion fish | source")
	}
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() != 1 {
		fs.Usage()
		return errors.New("one shell is to be named: bash, zsh or fish")
	}
	var script string
	switch fs.Arg(0) {
	case "bash":
		script = bashCompletion(completions(ctx))
	case "zsh":
		script = zshCompletion(completions(ctx))
	case "fish":
		script = fishCompletion(completions(ctx))
	default:
		return fmt.Errorf("unknown shell %q: bash, zsh or fish", fs.Arg(0))
	}
	_, err := io.WriteString(e.stdout, script)
	return err
}

// walk calls visit with each command and action that has words to
// complete after it, the commands first.
func walk(all []completed, visit func(c completed, words []string)) {
	top := []string{"-config", "help"}
	for _, c := range all {
		top = append(top, c.path)
	}
	visit(completed{}, top)
	for _, c := range all {
		var words []string
		for _, a := range c.actions {
			words = append(words, strings.TrimPrefix(a.path, c.path+" "))
		}
		for _, f := range c.flags {
			words = append(words, "-"+f.name)
		}
		visit(c, words)
		for _, a := range c.actions {
			var flags []string
			for _, f := range a.flags {
				flags = append(flags, "-"+f.name)
			}
			visit(a, flags)
		}
	}
}

// groups is the pattern of a shell's case that matches the commands with
// actions, and no command: the words a path is built of stop at an action
// or at a command without actions, and the rest are operands and values.
func groups(all []completed, quote string) string {
	names := []string{quote + quote}
	for _, c := range all {
		if c.actions != nil {
			names = append(names, c.path)
		}
	}
	return strings.Join(names, "|")
}

func bashCompletion(all []completed) string {
	var b strings.Builder
	b.WriteString(`# bash completion for freshgo: source <(freshgo completion bash)
_freshgo() {
	local cur="${COMP_WORDS[COMP_CWORD]}" path="" word words i
	if [[ ${COMP_WORDS[COMP_CWORD-1]} == -config ]]; then
		COMPREPLY=($(compgen -f -- "$cur"))
		return
	fi
	for ((i = 1; i < COMP_CWORD; i++)); do
		word="${COMP_WORDS[i]}"
		case "$word" in
		-config) ((i++)) ;;
		-*) ;;
		*) case "$path" in ` + groups(all, `"`) + `) path="${path:+$path }$word" ;; esac ;;
		esac
	done
	case "$path" in
`)
	walk(all, func(c completed, words []string) {
		fmt.Fprintf(&b, "\t%q) words=%q ;;\n", c.path, strings.Join(words, " "))
	})
	b.WriteString(`	*) words="" ;;
	esac
	COMPREPLY=($(compgen -W "$words" -- "$cur"))
}
complete -o default -F _freshgo freshgo
`)
	return b.String()
}

// zshQuote quotes s for zsh within single quotes.
func zshQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

func zshCompletion(all []completed) string {
	var b strings.Builder
	b.WriteString(`#compdef freshgo
# zsh completion for freshgo: source <(freshgo completion zsh)
_freshgo() {
	local path_="" word i
	local -a described
	if [[ ${words[CURRENT-1]} == -config ]]; then
		_files
		return
	fi
	for ((i = 2; i < CURRENT; i++)); do
		word=${words[i]}
		case $word in
		-config) ((i++)) ;;
		-*) ;;
		*) case $path_ in ` + groups(all, `'`) + `) path_="${path_:+$path_ }$word" ;; esac ;;
		esac
	done
	case $path_ in
`)
	describe := func(name, summary string) string {
		return zshQuote(strings.ReplaceAll(name, ":", `\:`) + ":" + summary)
	}
	top := []string{describe("-config", "read the settings from a YAML file"), describe("help", "list the commands")}
	for _, c := range all {
		top = append(top, describe(c.path, c.summary))
	}
	fmt.Fprintf(&b, "\t'') described=(%s) ;;\n", strings.Join(top, " "))
	for _, c := range all {
		var words []string
		for _, a := range c.actions {
			words = append(words, describe(strings.TrimPrefix(a.path, c.path+" "), a.summary))
		}
		for _, f := range c.flags {
			words = append(words, describe("-"+f.name, f.usage))
		}
		fmt.Fprintf(&b, "\t%s) described=(%s) ;;\n", zshQuote(c.path), strings.Join(words, " "))
		for _, a := range c.actions {
			var flags []string
			for _, f := range a.flags {
				flags = append(flags, describe("-"+f.name, f.usage))
			}
			fmt.Fprintf(&b, "\t%s) described=(%s) ;;\n", zshQuote(a.path), strings.Join(flags, " "))
		}
	}
	b.WriteString(`	esac
	if (( ${#described} )); then
		_describe 'freshgo' described
	else
		_files
	fi
}
compdef _freshgo freshgo
`)
	return b.String()
}

// fishQuote quotes s for fish within single quotes.
func fishQuote(s string) string {
	return "'" + strings.NewReplacer(`\`, `\\`, "'", `\'`).Replace(s) + "'"
}

func fishCompletion(all []completed) string {
	var b strings.Builder
	b.WriteString("# fish completion for freshgo: freshgo completion fish | source\n")
	var groupNames []string
	for _, c := range all {
		if c.actions != nil {
			groupNames = append(groupNames, c.path)
		}
	}
	top := "__fish_use_subcommand"
	fmt.Fprintf(&b, "complete -c freshgo -n %s -o config -r -F -d %s\n", fishQuote(top), fishQuote("read the settings from a YAML file"))
	fmt.Fprintf(&b, "complete -c freshgo -n %s -f -a help -d %s\n", fishQuote(top), fishQuote("list the commands"))
	for _, c := range all {
		fmt.Fprintf(&b, "complete -c freshgo -n %s -f -a %s -d %s\n", fishQuote(top), c.path, fishQuote(c.summary))
	}
	// An action may have the name of a command, as opml import has: the
	// flags of a command are offered without any command with actions.
	notGroup := "; and not __fish_seen_subcommand_from " + strings.Join(groupNames, " ")
	flagLines := func(condition string, flags []completedFlag) {
		for _, f := range flags {
			fmt.Fprintf(&b, "complete -c freshgo -n %s -o %s -d %s\n", fishQuote(condition), f.name, fishQuote(f.usage))
		}
	}
	for _, c := range all {
		if c.actions == nil {
			flagLines("__fish_seen_subcommand_from "+c.path+notGroup, c.flags)
			continue
		}
		var actionNames []string
		for _, a := range c.actions {
			actionNames = append(actionNames, strings.TrimPrefix(a.path, c.path+" "))
		}
		for _, a := range c.actions {
			name := strings.TrimPrefix(a.path, c.path+" ")
			condition := "__fish_seen_subcommand_from " + c.path + "; and not __fish_seen_subcommand_from " + strings.Join(actionNames, " ")
			fmt.Fprintf(&b, "complete -c freshgo -n %s -f -a %s -d %s\n", fishQuote(condition), name, fishQuote(a.summary))
			flagLines("__fish_seen_subcommand_from "+c.path+"; and __fish_seen_subcommand_from "+name, a.flags)
		}
	}
	return b.String()
}
