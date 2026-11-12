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
	"adhar-io/adhar/platform/utils"
	"errors"
	"fmt"
	"os"
	"strings"
)

// Shared status icons for consistent visual feedback across commands.
const (
	IconSuccess = "✓"
	IconError   = "✖"
	IconWarning = "▲"
	IconInfo    = "▸"
	IconHint    = "▸"
)

// friendlyError wraps an underlying error that has already been rendered to the
// user by FriendlyError. Callers (e.g. the root Execute) can detect it to avoid
// printing the raw error a second time, while still unwrapping to the original.
type friendlyError struct{ err error }

func (e *friendlyError) Error() string { return e.err.Error() }
func (e *friendlyError) Unwrap() error { return e.err }

// exitCodeError carries the process exit code a command wants, when it is not
// the usual 1. The outcome it reports has already been printed, so the root
// renders nothing for it unless it wraps an error of its own.
type exitCodeError struct {
	code int
	err  error
}

func (e *exitCodeError) Error() string {
	if e.err == nil {
		return fmt.Sprintf("exit %d", e.code)
	}
	return e.err.Error()
}

func (e *exitCodeError) Unwrap() error { return e.err }

// ExitCode returns an error that makes the CLI exit with code. `adhar batch
// run --wait` ends with the RUN's exit code (design 0025 D2), so a workflow
// step or a shell `&&` can read the outcome without parsing output; cobra
// alone turns every error into 1. err may be nil when the outcome was printed
// already; a non-nil err is rendered as any other.
func ExitCode(code int, err error) error {
	return &exitCodeError{code: code, err: err}
}

// ExitCodeOf reports the exit code an error asks for, and whether it asked.
func ExitCodeOf(err error) (int, bool) {
	var e *exitCodeError
	if errors.As(err, &e) {
		return e.code, true
	}
	return 0, false
}

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
	case strings.Contains(msg, "docker"), strings.Contains(msg, "podman"),
		strings.Contains(msg, "container engine"):
		eng := utils.DetectContainerEngine()
		if eng.Available {
			return fmt.Sprintf("%s is running. If this persists, check that it can create containers.", eng.Name)
		}
		return fmt.Sprintf("Start a container engine (%s), then retry.",
			strings.Join(utils.EngineNames(), ", "))
	case strings.Contains(msg, "permission"), strings.Contains(msg, "forbidden"):
		return "Check your credentials and permissions for this cluster."
	default:
		return ""
	}
}
