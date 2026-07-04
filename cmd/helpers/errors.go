/*
Copyright 2025.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package helpers

import (
	"fmt"
	"os"
	"strings"
)

// Shared status icons for consistent visual feedback across commands.
const (
	IconSuccess = "✅"
	IconError   = "❌"
	IconWarning = "⚠️"
	IconInfo    = "ℹ️"
	IconHint    = "💡"
)

// friendlyError wraps an underlying error that has already been rendered to the
// user by FriendlyError. Callers (e.g. the root Execute) can detect it to avoid
// printing the raw error a second time, while still unwrapping to the original.
type friendlyError struct{ err error }

func (e *friendlyError) Error() string { return e.err.Error() }
func (e *friendlyError) Unwrap() error { return e.err }

// IsFriendlyError reports whether err (or anything it wraps) was already
// rendered by FriendlyError, so the top-level handler can skip re-printing it.
func IsFriendlyError(err error) bool {
	for err != nil {
		if _, ok := err.(*friendlyError); ok {
			return true
		}
		u, ok := err.(interface{ Unwrap() error })
		if !ok {
			return false
		}
		err = u.Unwrap()
	}
	return false
}

// FriendlyError prints a consistently styled, actionable error to stderr and
// returns an error suitable for returning from a cobra RunE. The original error
// is preserved in the returned value so callers and wrappers can still inspect
// it, while the human-friendly rendering (styled message + hint) is what the
// user sees. Pass an empty hint to omit the hint line.
//
// Typical usage in a command:
//
//	if err != nil {
//	    return helpers.FriendlyError(err, "Is the cluster running? Try: adhar up")
//	}
//
// To avoid Cobra printing the raw error a second time, set
// cmd.SilenceErrors = true (or SilenceUsage) on the command.
func FriendlyError(err error, hint string) error {
	if err == nil {
		return nil
	}

	fmt.Fprintln(os.Stderr, ErrorStyle.Render(IconError+" "+firstLine(err.Error())))

	// Surface a smart hint: prefer the caller-supplied hint, otherwise try to
	// infer one from the error text so common failures are still actionable.
	if hint == "" {
		hint = inferHint(err)
	}
	if hint != "" {
		fmt.Fprintln(os.Stderr, "   "+CreateMuted(IconHint+" "+hint))
	}

	return &friendlyError{err: err}
}

// firstLine returns the first line of a (possibly multi-line) error message so
// the headline stays compact; wrapped detail is still available via the error.
func firstLine(s string) string {
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		return strings.TrimSpace(s[:i])
	}
	return strings.TrimSpace(s)
}

// inferHint maps common low-level failures to a friendly, actionable suggestion.
func inferHint(err error) string {
	msg := strings.ToLower(err.Error())
	switch {
	case strings.Contains(msg, "connection refused"),
		strings.Contains(msg, "no such host"),
		strings.Contains(msg, "could not connect"),
		strings.Contains(msg, "the server could not find the requested resource") && strings.Contains(msg, "localhost"):
		return "Is the cluster running? Try: adhar up"
	case strings.Contains(msg, "kubeconfig"),
		strings.Contains(msg, "no configuration has been provided"),
		strings.Contains(msg, "unable to load"):
		return "No reachable cluster found. Start one with: adhar up"
	case strings.Contains(msg, "docker"):
		return "Make sure Docker is installed and running, then retry."
	case strings.Contains(msg, "permission"), strings.Contains(msg, "forbidden"):
		return "Check your credentials and permissions for this cluster."
	default:
		return ""
	}
}
