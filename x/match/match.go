// Package match defines functions for matching strings.
package match

import (
	"bytes"
	"regexp"
	"strings"
)

// A Func reports whether its argument matches a certain condition.
type Func = func(string) bool

// Any is a Func that matches any input.
func Any(string) bool { return true }

// Exactly returns a Func that matches exactly s.
func Exactly(s string) Func {
	return func(needle string) bool { return needle == s }
}

// AnyOrGlob returns a Func that behaves as match.Any if pattern == "",
// or otherwise as match.Glob(pattern).
func AnyOrGlob(pattern string) Func {
	if pattern == "" {
		return Any
	}
	return Glob(pattern)
}

// Glob returns a Func that accepts strings matching the given glob
// pattern. Within a pattern, "*" matches a run of arbitrary characters.
// The pattern "" matches only the empty string.
func Glob(pattern string) Func {
	// Shortcut a couple of trivial cases.
	if !strings.Contains(pattern, "*") {
		return Exactly(pattern)
	} else if pattern == "*" {
		return func(string) bool { return true }
	}

	// Compile the glob into a regeular expression.
	// Everything is escaped except the glob star, so compilation can't fail.
	segments := strings.Split(pattern, "*")
	var buf bytes.Buffer
	buf.WriteString("^") // anchor at front
	for i, seg := range segments {
		buf.WriteString(regexp.QuoteMeta(seg))
		if i+1 < len(segments) {
			buf.WriteString(".*")
		}
	}
	buf.WriteString("$") // anchor at end
	return regexp.MustCompile(buf.String()).MatchString
}
