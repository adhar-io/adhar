package globals

import "strings"

// PlatformEnvironments returns the canonical promotion chain in order:
// dev, test, prod.
//
// Returned as a fresh slice on every call so a caller cannot mutate the
// platform's idea of its own environments — the drift this list exists to
// prevent would otherwise be one careless append away.
func PlatformEnvironments() []string {
	return strings.Split(PlatformEnvironmentsCSV, ",")
}

// DefaultEnvironment is where a newly scaffolded application is published
// first: the head of the promotion chain. Nothing is promoted INTO dev, so an
// app that has never been promoted lives there.
func DefaultEnvironment() string {
	return PlatformEnvironments()[0]
}
