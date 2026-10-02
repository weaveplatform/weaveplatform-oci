package spec

import (
	"errors"
	"fmt"
	"strings"
)

// ErrInvalid is wrapped by every validation failure reported by this package.
var ErrInvalid = errors.New("artifact does not conform to the weave guest contract v1")

// ErrNoMatchingPlatform is returned by SelectChild when no child matches.
var ErrNoMatchingPlatform = errors.New("no index child matches the requested platform")

// Problem is one conformance failure. Rule is the rule number from the
// contract's conformance checklist (§12); zero means a structural problem
// found before the checklist applies (unparseable JSON, for example).
type Problem struct {
	Rule    int
	Path    string
	Message string
}

func (p Problem) String() string {
	if p.Path == "" {
		return fmt.Sprintf("rule %d: %s", p.Rule, p.Message)
	}
	return fmt.Sprintf("rule %d: %s: %s", p.Rule, p.Path, p.Message)
}

// ValidationError collects every problem found; validators report all
// failures rather than stopping at the first.
type ValidationError struct {
	Problems []Problem
}

func (e *ValidationError) Error() string {
	parts := make([]string, 0, len(e.Problems))
	for _, p := range e.Problems {
		parts = append(parts, p.String())
	}
	return ErrInvalid.Error() + ": " + strings.Join(parts, "; ")
}

// Unwrap lets callers test errors.Is(err, ErrInvalid).
func (e *ValidationError) Unwrap() error { return ErrInvalid }

// HasRule reports whether a problem with the given rule number was recorded.
func (e *ValidationError) HasRule(rule int) bool {
	for _, p := range e.Problems {
		if p.Rule == rule {
			return true
		}
	}
	return false
}

// problems accumulates Problems and converts them to an error.
type problems []Problem

func (ps *problems) add(rule int, path, format string, args ...any) {
	*ps = append(*ps, Problem{Rule: rule, Path: path, Message: fmt.Sprintf(format, args...)})
}

func (ps problems) err() error {
	if len(ps) == 0 {
		return nil
	}
	return &ValidationError{Problems: ps}
}

// Problems extracts the problems from an error returned by this package.
func Problems(err error) []Problem {
	var ve *ValidationError
	if errors.As(err, &ve) {
		return ve.Problems
	}
	return nil
}
