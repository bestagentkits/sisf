// Command sisf is the command line of one factory deployment.
//
// This file holds the command registry and nothing else. Each command lives
// in its own file, cmd_<area>.go, and adds itself to the registry from
// init(), so a new command never edits this file.
//
// Exit status: 0 on success, 1 when a command fails, 2 when it was invoked
// wrongly, including an option value a lower layer refused as invalid. A
// command may choose another status by returning an error with an
// ExitCode method.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"regexp"
	"slices"
	"sort"
	"strings"
	"syscall"

	"github.com/bestagentkits/sisf/internal/install"
	"github.com/bestagentkits/sisf/pkg/core"
)

// Exit statuses of the program.
const (
	exitOK      = 0
	exitFailure = 1
	exitUsage   = 2
)

// program is the name the CLI is invoked by.
const program = "sisf"

// Env is where a command reads input and writes output. Commands use it
// instead of the process streams so they can be run in tests.
type Env struct {
	Stdin  io.Reader
	Stdout io.Writer
	Stderr io.Writer
}

// Printf writes a command's result to standard output. A result that cannot
// be written was not delivered, so the failure is returned for the command to
// pass on instead of exiting 0.
func (e *Env) Printf(format string, args ...any) error {
	if _, err := fmt.Fprintf(e.Stdout, format, args...); err != nil {
		return core.NewError(core.CodeUnavailable, "the result could not be written to standard output").WithCause(err)
	}
	return nil
}

// say writes help or a diagnostic. A failed write is dropped on purpose: the
// stream that would carry the complaint is the one that failed, and the exit
// status already tells the caller what happened.
func say(w io.Writer, format string, args ...any) {
	_, _ = fmt.Fprintf(w, format, args...)
}

// Command is one command of the CLI.
type Command struct {
	// Path is the words that select the command, for example
	// {"conformance", "check"}. Commands sharing leading words form a group.
	Path []string
	// Args describes what follows the path, for example "[file]".
	Args string
	// Summary is one line for the command list.
	Summary string
	// Help is optional further text shown by "sisf help <command>".
	Help string
	// Flags registers the command's flags, for the flag reference of
	// "sisf help <command>". The command's Run binds the same flags.
	Flags func(fs *flag.FlagSet)
	// Run executes the command with the arguments after its path. The
	// context is cancelled when the process is interrupted. Returning an
	// error made by usagef reports a mistake in the invocation; returning
	// flag.ErrHelp shows the command's help.
	Run func(ctx context.Context, env *Env, args []string) error
}

// name returns the command's path as typed, without the program name.
func (c Command) name() string { return strings.Join(c.Path, " ") }

// synopsis returns the usage line of the command.
func (c Command) synopsis() string {
	return strings.TrimSpace(program + " " + c.name() + " " + c.Args)
}

// usageError marks a mistake in how a command was invoked.
type usageError struct{ message string }

func (e *usageError) Error() string { return e.message }

// usagef reports a mistake in the invocation: wrong arguments or flags. The
// CLI answers it with the command's usage line and exit status 2.
func usagef(format string, args ...any) error {
	for i, arg := range args {
		if err, ok := arg.(error); ok {
			args[i] = errorMessage(err) // a code reads like a crash beside a flag name
		}
	}
	return &usageError{message: fmt.Sprintf(format, args...)}
}

// exitCoder is implemented by an error that chooses the exit status.
type exitCoder interface{ ExitCode() int }

// silent is implemented by an error the command already reported in full,
// such as the doctor's failed checks; only its exit status is left to set.
type silent interface{ Silent() bool }

// registry holds the commands of the CLI.
type registry struct{ commands []Command }

// commands is the registry every cmd_<area>.go file adds itself to.
var commands registry

// register adds a command to the CLI. It is called from init() and panics on
// a command that could never be invoked, so the mistake surfaces at startup.
func register(c Command) { commands.add(c) }

var wordPattern = regexp.MustCompile(`^[a-z][a-z0-9-]*$`)

// add validates and stores a command.
func (r *registry) add(c Command) {
	switch {
	case len(c.Path) == 0 || c.Run == nil || c.Summary == "":
		panic(fmt.Sprintf("%s: command %q needs a path, a summary and a Run function", program, c.name()))
	case c.Path[0] == "help":
		panic(fmt.Sprintf("%s: command %q would hide the built-in help", program, c.name()))
	}
	for _, word := range c.Path {
		if !wordPattern.MatchString(word) {
			panic(fmt.Sprintf("%s: command %q has a word that is not lowercase letters, digits and dashes", program, c.name()))
		}
	}
	for _, existing := range r.commands {
		if slices.Equal(existing.Path, c.Path) {
			panic(fmt.Sprintf("%s: command %q is registered twice", program, c.name()))
		}
	}
	r.commands = append(r.commands, c)
	sort.SliceStable(r.commands, func(i, j int) bool { return r.commands[i].name() < r.commands[j].name() })
}

// resolve finds the command whose path is the longest prefix of args and
// returns it with the arguments that follow the path.
func (r *registry) resolve(args []string) (*Command, []string) {
	var found *Command
	for i := range r.commands {
		c := &r.commands[i]
		if len(c.Path) <= len(args) && slices.Equal(c.Path, args[:len(c.Path)]) && (found == nil || len(c.Path) > len(found.Path)) {
			found = c
		}
	}
	if found == nil {
		return nil, args
	}
	return found, args[len(found.Path):]
}

// under returns the commands whose path starts with prefix.
func (r *registry) under(prefix []string) []Command {
	var group []Command
	for _, c := range r.commands {
		if len(c.Path) >= len(prefix) && slices.Equal(c.Path[:len(prefix)], prefix) {
			group = append(group, c)
		}
	}
	return group
}

// isHelpFlag reports whether arg asks for help.
func isHelpFlag(arg string) bool { return arg == "-h" || arg == "-help" || arg == "--help" }

// isVersionFlag reports whether arg asks for the version.
func isVersionFlag(arg string) bool { return arg == "--version" || arg == "-version" || arg == "-v" }

// run executes the command line args and returns the exit status.
func (r *registry) run(ctx context.Context, env *Env, args []string) int {
	switch {
	case len(args) == 0:
		r.list(env.Stderr, nil)
		return exitUsage
	case isHelpFlag(args[0]):
		r.list(env.Stdout, nil)
		return exitOK
	case isVersionFlag(args[0]):
		say(env.Stdout, "%s\n", versionLine())
		return exitOK
	case args[0] == "help":
		return r.help(env, args[1:])
	}
	cmd, rest := r.resolve(args)
	if cmd == nil {
		return r.unknown(env, args)
	}
	if len(rest) > 0 && isHelpFlag(rest[0]) {
		describe(env.Stdout, *cmd)
		return exitOK
	}
	return report(env, *cmd, cmd.Run(ctx, env, rest))
}

// help answers "sisf help [command]".
func (r *registry) help(env *Env, args []string) int {
	if len(args) == 0 {
		r.list(env.Stdout, nil)
		return exitOK
	}
	// Words after a command are its arguments, not another command.
	if cmd, _ := r.resolve(args); cmd != nil {
		describe(env.Stdout, *cmd)
		return exitOK
	}
	if len(r.under(args)) > 0 {
		r.list(env.Stdout, args)
		return exitOK
	}
	return r.unknown(env, args)
}

// unknown reports arguments that select no command. When they start with a
// group it names the word that is not one of its commands, suggests the
// nearest, and lists the group's commands.
func (r *registry) unknown(env *Env, args []string) int {
	for n := len(args); n > 0; n-- {
		group := r.under(args[:n])
		if len(group) == 0 {
			continue
		}
		name := strings.Join(args[:n], " ")
		if n < len(args) {
			say(env.Stderr, "%s: %q is not a command of %s%s\n\n", program, args[n], name, suggest(args[n], group, n))
		} else {
			say(env.Stderr, "%s: %q is a group of commands; name one of them\n\n", program, name)
		}
		r.list(env.Stderr, args[:n])
		return exitUsage
	}
	var top []Command
	for _, c := range r.commands {
		top = append(top, Command{Path: c.Path[:1]})
	}
	say(env.Stderr, "%s: unknown command %q%s\nRun \"%s help\" for the list of commands.\n", program, args[0], suggest(args[0], top, 0), program)
	return exitUsage
}

// suggest returns "; did you mean X?" for the word of the commands at
// position at that is nearest to word, when one is close enough.
func suggest(word string, commands []Command, at int) string {
	best, bestDistance := "", 3
	for _, c := range commands {
		if len(c.Path) <= at {
			continue
		}
		if d := editDistance(word, c.Path[at]); d < bestDistance {
			best, bestDistance = c.Path[at], d
		}
	}
	if best == "" {
		return ""
	}
	return "; did you mean " + best + "?"
}

// editDistance is the Levenshtein distance between a and b.
func editDistance(a, b string) int {
	prev := make([]int, len(b)+1)
	for j := range prev {
		prev[j] = j
	}
	for i := 1; i <= len(a); i++ {
		cur := make([]int, len(b)+1)
		cur[0] = i
		for j := 1; j <= len(b); j++ {
			cost := 1
			if a[i-1] == b[j-1] {
				cost = 0
			}
			cur[j] = min(prev[j]+1, cur[j-1]+1, prev[j-1]+cost)
		}
		prev = cur
	}
	return prev[len(b)]
}

// list prints the commands under prefix, or all of them. The whole list
// opens with what sisf is; a group's list shows its commands only.
func (r *registry) list(w io.Writer, prefix []string) {
	group := r.under(prefix)
	width := 0
	for _, c := range group {
		width = max(width, len(c.name()))
	}
	if len(prefix) == 0 {
		say(w, "%s is the command line of one Self-improvement Software Factory deployment.\n\n", program)
		say(w, "Usage:\n  %s <command> [arguments]\n  %s --version\n\nCommands:\n", program, program)
	} else {
		say(w, "Commands of %s %s:\n", program, strings.Join(prefix, " "))
	}
	for _, c := range group {
		say(w, "  %-*s  %s\n", width, c.name(), c.Summary)
	}
	say(w, "\nRun \"%s help <command>\" for its arguments and flags.\n", program)
}

// describe prints the help of one command, ending with its flag reference.
func describe(w io.Writer, c Command) {
	say(w, "%s\n\nUsage:\n  %s\n", c.Summary, c.synopsis())
	if c.Help != "" {
		say(w, "\n%s\n", strings.TrimSpace(c.Help))
	}
	if c.Flags == nil {
		return
	}
	fs := flag.NewFlagSet(c.name(), flag.ContinueOnError)
	c.Flags(fs)
	var rows [][2]string
	width := 0
	fs.VisitAll(func(f *flag.Flag) {
		kind, usage := flag.UnquoteUsage(f)
		left := "--" + f.Name
		if len(f.Name) == 1 {
			left = "-" + f.Name // as the usage lines write a one-letter flag, such as down's -v
		}
		if kind != "" {
			left += " " + kind
		}
		if f.DefValue != "" && f.DefValue != "false" && f.DefValue != "0" && f.DefValue != "[]" {
			usage += " (default " + f.DefValue + ")"
		}
		rows = append(rows, [2]string{left, usage})
		width = max(width, len(left))
	})
	if len(rows) == 0 {
		return
	}
	say(w, "\nFlags:\n")
	for _, row := range rows {
		say(w, "  %-*s  %s\n", width, row[0], row[1])
	}
}

// report turns the result of a command into output and an exit status.
func report(env *Env, cmd Command, err error) int {
	var usage *usageError
	switch {
	case err == nil:
		return exitOK
	case errors.Is(err, flag.ErrHelp):
		describe(env.Stdout, cmd)
		return exitOK
	case errors.As(err, &usage):
		say(env.Stderr, "%s %s: %s\nUsage: %s\n", program, cmd.name(), usage.message, cmd.synopsis())
		return exitUsage
	}
	var quiet silent
	if !errors.As(err, &quiet) || !quiet.Silent() {
		printError(env.Stderr, err)
	}
	var coded exitCoder
	if errors.As(err, &coded) {
		return coded.ExitCode()
	}
	return exitFailure
}

// printError writes a failure in the typed form every surface uses: the
// error code, the message, any details and the steps the caller may take
// next. An error without a code is reported as INTERNAL.
func printError(w io.Writer, err error) {
	var typed *core.Error
	if !errors.As(err, &typed) {
		say(w, "%s: %s: %v\n", program, core.CodeInternal, err)
		return
	}
	say(w, "%s: %s\n", program, typed.Error())
	keys := make([]string, 0, len(typed.Details))
	for key := range typed.Details {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, key := range keys {
		say(w, "  %s: %s\n", key, typed.Details[key])
	}
	for _, next := range typed.NextActions {
		say(w, "  next: %s", next.Action)
		if next.Description != "" {
			say(w, " - %s", next.Description)
		}
		say(w, "\n")
	}
}

func main() {
	// `sisf up` starts `sisf sim ...` as units, which inherit its run lock;
	// nothing this program starts may hold it.
	install.KeepInheritedRunLockFromChildren()
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	status := commands.run(ctx, &Env{Stdin: os.Stdin, Stdout: os.Stdout, Stderr: os.Stderr}, os.Args[1:])
	stop()
	os.Exit(status)
}
