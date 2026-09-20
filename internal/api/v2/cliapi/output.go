package cliapi

import (
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"slices"
	"strings"
	"time"

	"go.klarlabs.de/rollops/internal/api/v2/apierr"
	"go.klarlabs.de/rollops/internal/api/v2/page"
)

// Format is how an answer is written. Spec 26.3 requires the machine one on
// every read and plan command, so it is a flag on all of them rather than a
// mode the CLI is started in — a script must be able to ask for JSON without
// knowing how the operator's shell is configured.
type Format string

const (
	// FormatText is the concise, decision-oriented output of spec 26.2.
	FormatText Format = "text"

	// FormatJSON is the stable document of spec 26.3. It carries no terminal
	// formatting, which is why nothing in this package writes an escape
	// sequence at all rather than stripping them on the way out: output that
	// has to be cleaned is output that will one day be forgotten.
	FormatJSON Format = "json"
)

// invalid marks a mistake in the command line as the caller's, so that it
// exits as a validation error rather than as a system failure. Usage is the
// one failure a CLI produces without ever reaching the service, and it would
// otherwise fall through apierr's INTERNAL default.
func invalid(format string, args ...any) error {
	err := fmt.Errorf(format, args...)
	return &apierr.Error{Code: apierr.InvalidArgument, Message: err.Error(), Err: err}
}

// flags builds a flag set that reports rather than prints. The stdlib default
// writes usage to stderr and, for flag.ExitOnError, ends the process — neither
// of which leaves an exit code this package chose.
func flags(name string) *flag.FlagSet {
	fs := flag.NewFlagSet(name, flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	return fs
}

// parse reads the flags and returns what was left over, in the order it was
// given.
//
// It parses repeatedly because the stdlib stops at the first non-flag word, and
// an operator who writes `rollops status dep_1 --output json` means the flag
// rather than a third argument. Re-parsing past each positional is the standard
// way to allow the two to interleave without writing a second parser.
func parse(fs *flag.FlagSet, args []string) ([]string, error) {
	var positional []string
	for {
		if err := fs.Parse(args); err != nil {
			return nil, invalid("%s: %w", fs.Name(), err)
		}
		rest := fs.Args()
		if len(rest) == 0 {
			return positional, nil
		}
		positional = append(positional, rest[0])
		args = rest[1:]
	}
}

// list renders a set of names for a message.
func list(names []string) string { return strings.Join(names, ", ") }

// outputFlag registers --output and returns where the choice lands.
func outputFlag(fs *flag.FlagSet) *string {
	var s string
	fs.StringVar(&s, "output", string(FormatText), "text or json")
	return &s
}

func format(s string) (Format, error) {
	switch Format(s) {
	case FormatText:
		return FormatText, nil
	case FormatJSON:
		return FormatJSON, nil
	default:
		return "", invalid("--output %q: want text or json", s)
	}
}

// pageFlags registers the cursor and size every listing takes.
func pageFlags(fs *flag.FlagSet) *page.Request {
	var req page.Request
	fs.StringVar(&req.Cursor, "page-token", "", "opaque cursor from a previous call")
	fs.IntVar(&req.Size, "page-size", 0, "most rows wanted; clamped to the server's maximum")
	return &req
}

// render writes v as the chosen format. The human branch is a closure rather
// than a method on the view, so that what a view means to a person lives beside
// the command that asked for it and the JSON shape stays free of rendering.
func (a *App) render(f Format, v any, human func(w io.Writer)) error {
	if f == FormatJSON {
		enc := json.NewEncoder(a.out)
		enc.SetIndent("", "  ")
		if err := enc.Encode(v); err != nil {
			return fmt.Errorf("writing output: %w", err)
		}
		return nil
	}
	human(a.out)
	return nil
}

// printf writes a line of human output. Write errors are dropped: a CLI whose
// stdout has gone away has nowhere to report that it has gone away, and the
// work it was reporting on already happened.
func printf(w io.Writer, format string, args ...any) {
	_, _ = fmt.Fprintf(w, format, args...)
}

// nextPage tells a person how to ask for the rest. Machine output carries the
// cursor as a field instead, so this is text-only.
func nextPage(w io.Writer, next string) {
	if next != "" {
		printf(w, "more: --page-token %s\n", next)
	}
}

// kvFlag collects repeated key=value pairs, as labels and metadata arrive.
type kvFlag map[string]string

func (k *kvFlag) String() string { return "" }

func (k *kvFlag) Set(s string) error {
	key, value, ok := strings.Cut(s, "=")
	if !ok || key == "" {
		return fmt.Errorf("want key=value, got %q", s)
	}
	if *k == nil {
		*k = kvFlag{}
	}
	(*k)[key] = value
	return nil
}

// listFlag collects a repeated flag in the order it was given.
type listFlag []string

func (l *listFlag) String() string { return "" }

func (l *listFlag) Set(s string) error {
	*l = append(*l, s)
	return nil
}

// fields parses the comma-separated key=value form repeated flags use for
// anything with more than one part — a target, a policy, an artifact binding.
// One flag per part would multiply into a surface nobody could remember, and
// positional syntax would make adding a part a breaking change.
func fields(spec string) (map[string]string, error) {
	out := map[string]string{}
	for _, part := range strings.Split(spec, ",") {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		key, value, ok := strings.Cut(part, "=")
		if !ok || key == "" {
			return nil, fmt.Errorf("want key=value, got %q", part)
		}
		out[key] = value
	}
	return out, nil
}

// only refuses a field nobody reads, rather than ignoring it. A misspelt key in
// a target spec would otherwise create an environment quietly missing the
// setting the operator thought they had given it.
func only(got map[string]string, known ...string) error {
	for k := range got {
		if !slices.Contains(known, k) {
			return fmt.Errorf("unknown field %q (want one of %s)", k, strings.Join(known, ", "))
		}
	}
	return nil
}

// rfc3339 is the one timestamp format human output uses. A CLI that rendered
// times in the local locale would produce output nobody could sort.
const rfc3339 = time.RFC3339
