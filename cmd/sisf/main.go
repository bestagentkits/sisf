// Command sisf is the command line of one factory deployment.
//
// This file holds the command registry and nothing else. Each command lives
// in its own file, cmd_<area>.go, and adds itself to the registry from
// init(), so a new command never edits this file.
//
// Exit status: 0 on success, 1 when a command fails, 2 when it was invoked
// wrongly. A command may choose another status by returning an error with an
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

	"github.com/bestagentkits/sisf/internal/core"
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
	return &usageError{message: fmt.Sprintf(format, args...)}
}

// exitCoder is implemented by an error that chooses the exit status.
type exitCoder interface{ ExitCode() int }

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

// run executes the command line args and returns the exit status.
func (r *registry) run(ctx context.Context, env *Env, args []string) int {
	switch {
	case len(args) == 0:
		r.list(env.Stderr, nil)
		return exitUsage
	case isHelpFlag(args[0]):
		r.list(env.Stdout, nil)
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
	if cmd, rest := r.resolve(args); cmd != nil && len(rest) == 0 {
		describe(env.Stdout, *cmd)
		return exitOK
	}
	if len(r.under(args)) > 0 {
		r.list(env.Stdout, args)
		return exitOK
	}
	return r.unknown(env, args)
}

// unknown reports arguments that select no command. When they name a group
// it lists the group's commands.
func (r *registry) unknown(env *Env, args []string) int {
	for n := len(args); n > 0; n-- {
		if len(r.under(args[:n])) > 0 {
			say(env.Stderr, "%s: %q is not a command by itself\n\n", program, strings.Join(args[:n], " "))
			r.list(env.Stderr, args[:n])
			return exitUsage
		}
	}
	say(env.Stderr, "%s: unknown command %q\nRun \"%s help\" for the list of commands.\n", program, args[0], program)
	return exitUsage
}

// list prints the commands under prefix, or all of them.
func (r *registry) list(w io.Writer, prefix []string) {
	group := r.under(prefix)
	width := 0
	for _, c := range group {
		width = max(width, len(strings.TrimSpace(c.name()+" "+c.Args)))
	}
	say(w, "%s is the command line of one Self-improvement Software Factory deployment.\n\n", program)
	say(w, "Usage:\n  %s <command> [arguments]\n\nCommands:\n", program)
	for _, c := range group {
		say(w, "  %-*s  %s\n", width, strings.TrimSpace(c.name()+" "+c.Args), c.Summary)
	}
	say(w, "\nRun \"%s help <command>\" for details.\n", program)
}

// describe prints the help of one command.
func describe(w io.Writer, c Command) {
	say(w, "%s\n\nUsage:\n  %s\n", c.Summary, c.synopsis())
	if c.Help != "" {
		say(w, "\n%s\n", strings.TrimSpace(c.Help))
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
	printError(env.Stderr, err)
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
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	status := commands.run(ctx, &Env{Stdin: os.Stdin, Stdout: os.Stdout, Stderr: os.Stderr}, os.Args[1:])
	stop()
	os.Exit(status)
}
