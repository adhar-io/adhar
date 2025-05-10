package providers

import (
	"adhar-io/adhar/platform/config"

	tea "github.com/charmbracelet/bubbletea"
)

// Provisioner defines the interface for provisioning environments
// Updated to include configuration from adhar-config.yaml
type Provisioner interface {
	Provision(config *config.ResolvedEnvironmentConfig) error
}

// --- Message types for status updates (can be shared or adapted) ---

// StepMsg signals a change in the major step of the provisioning process.
type StepMsg string

// StatusMsg provides a detailed status update within a step.
type StatusMsg string

// ErrorMsg signals an error occurred during provisioning.
type ErrorMsg struct{ Err error }

// DoneMsg signals the successful completion of the provisioning process.
type DoneMsg struct{}

// ClusterInfoMsg provides detailed cluster information.
type ClusterInfoMsg string

// Send is a helper function to send messages to the UI channel.
// It needs to be accessible or passed to the provider implementations.
func Send(ch chan<- tea.Msg, msg tea.Msg) {
	if ch != nil {
		ch <- msg
	}
}
