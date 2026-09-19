// Package name validates the human-readable aliases the domain uses for
// projects, environments, target bindings and policy bindings.
//
// A name is an alias, not identity (spec 4.2) — but it is what appears in a
// CLI argument, a URL path and a config file, so it must be unambiguous in all
// three. One rule covers every case so that a name valid in one surface cannot
// be rejected by another.
package name

import (
	"errors"
	"fmt"
	"regexp"
	"strings"
)

// ErrInvalid reports a name that cannot be used as an alias.
var ErrInvalid = errors.New("invalid name")

// MaxLen keeps a name inside a DNS label, which is the tightest place one is
// likely to end up.
const MaxLen = 63

// pattern admits lower case alphanumerics separated by single hyphens. It
// excludes the underscore, which is what keeps a typed ID from being accepted
// as a name.
var pattern = regexp.MustCompile(`^[a-z0-9]+(-[a-z0-9]+)*$`)

// Validate reports whether n is a usable alias. what names the thing being
// named, so the error reads as a sentence at the point it surfaces.
func Validate(what, n string) error {
	switch {
	case strings.TrimSpace(n) == "":
		return fmt.Errorf("%w: a %s has no name", ErrInvalid, what)
	case len(n) > MaxLen:
		return fmt.Errorf("%w: %s %q is longer than %d characters", ErrInvalid, what, n, MaxLen)
	case !pattern.MatchString(n):
		return fmt.Errorf("%w: %s %q is not lower case alphanumerics separated by hyphens", ErrInvalid, what, n)
	}
	return nil
}
