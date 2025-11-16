package provider

import (
	"fmt"
	"time"
)

// Error types for better error handling
type (
	// ClusterNotFoundError indicates a cluster was not found
	ClusterNotFoundError struct {
		ClusterID string
		Provider  string
	}

	// AuthenticationError indicates authentication failed
	AuthenticationError struct {
		Provider string
		Reason   string
	}

	// ResourceCreationError indicates resource creation failed
	ResourceCreationError struct {
		ResourceType string
		ResourceName string
		Provider     string
		Reason       string
	}

	// ResourceDeletionError indicates resource deletion failed
	ResourceDeletionError struct {
		ResourceType string
		ResourceID   string
		Provider     string
		Reason       string
	}

	// ConfigurationError indicates configuration is invalid
	ConfigurationError struct {
		Field    string
		Value    interface{}
		Reason   string
		Provider string
	}

	// TimeoutError indicates an operation timed out
	TimeoutError struct {
		Operation string
		Duration  time.Duration
		Provider  string
	}

	// QuotaExceededError indicates quota limits were exceeded
	QuotaExceededError struct {
		ResourceType string
		Provider     string
		Region       string
	}

	// NetworkError indicates a network-related error
	NetworkError struct {
		Operation string
		Provider  string
		Reason    string
	}

	// ValidationError indicates validation failed
	ValidationError struct {
		Field    string
		Value    interface{}
		Expected string
		Provider string
	}
)

// Error implementations
func (e *ClusterNotFoundError) Error() string {
	return fmt.Sprintf("cluster '%s' not found in provider '%s'", e.ClusterID, e.Provider)
}

func (e *AuthenticationError) Error() string {
	return fmt.Sprintf("authentication failed for provider '%s': %s", e.Provider, e.Reason)
}

func (e *ResourceCreationError) Error() string {
	return fmt.Sprintf("failed to create %s '%s' in provider '%s': %s", e.ResourceType, e.ResourceName, e.Provider, e.Reason)
}

func (e *ResourceDeletionError) Error() string {
	return fmt.Sprintf("failed to delete %s '%s' in provider '%s': %s", e.ResourceType, e.ResourceID, e.Provider, e.Reason)
}

func (e *ConfigurationError) Error() string {
	return fmt.Sprintf("invalid configuration for provider '%s': field '%s' with value '%v': %s", e.Provider, e.Field, e.Value, e.Reason)
}

func (e *TimeoutError) Error() string {
	return fmt.Sprintf("operation '%s' timed out after %v in provider '%s'", e.Operation, e.Duration, e.Provider)
}

func (e *QuotaExceededError) Error() string {
	return fmt.Sprintf("quota exceeded for %s in provider '%s' region '%s'", e.ResourceType, e.Provider, e.Region)
}

func (e *NetworkError) Error() string {
	return fmt.Sprintf("network error during '%s' in provider '%s': %s", e.Operation, e.Provider, e.Reason)
}

func (e *ValidationError) Error() string {
	return fmt.Sprintf("validation failed for provider '%s': field '%s' with value '%v', expected %s", e.Provider, e.Field, e.Value, e.Expected)
}

// Error constructors
func NewClusterNotFoundError(clusterID, provider string) *ClusterNotFoundError {
	return &ClusterNotFoundError{
		ClusterID: clusterID,
		Provider:  provider,
	}
}

func NewAuthenticationError(provider, reason string) *AuthenticationError {
	return &AuthenticationError{
		Provider: provider,
		Reason:   reason,
	}
}

func NewResourceCreationError(resourceType, resourceName, provider, reason string) *ResourceCreationError {
	return &ResourceCreationError{
		ResourceType: resourceType,
		ResourceName: resourceName,
		Provider:     provider,
		Reason:       reason,
	}
}

func NewResourceDeletionError(resourceType, resourceID, provider, reason string) *ResourceDeletionError {
	return &ResourceDeletionError{
		ResourceType: resourceType,
		ResourceID:   resourceID,
		Provider:     provider,
		Reason:       reason,
	}
}

func NewConfigurationError(field string, value interface{}, provider, reason string) *ConfigurationError {
	return &ConfigurationError{
		Field:    field,
		Value:    value,
		Provider: provider,
		Reason:   reason,
	}
}

func NewTimeoutError(operation string, duration time.Duration, provider string) *TimeoutError {
	return &TimeoutError{
		Operation: operation,
		Duration:  duration,
		Provider:  provider,
	}
}

func NewQuotaExceededError(resourceType, provider, region string) *QuotaExceededError {
	return &QuotaExceededError{
		ResourceType: resourceType,
		Provider:     provider,
		Region:       region,
	}
}

func NewNetworkError(operation, provider, reason string) *NetworkError {
	return &NetworkError{
		Operation: operation,
		Provider:  provider,
		Reason:    reason,
	}
}

func NewValidationError(field string, value interface{}, expected, provider string) *ValidationError {
	return &ValidationError{
		Field:    field,
		Value:    value,
		Expected: expected,
		Provider: provider,
	}
}

// IsErrorType checks if an error is of a specific type
func IsErrorType(err error, errorType interface{}) bool {
	switch errorType.(type) {
	case *ClusterNotFoundError:
		_, ok := err.(*ClusterNotFoundError)
		return ok
	case *AuthenticationError:
		_, ok := err.(*AuthenticationError)
		return ok
	case *ResourceCreationError:
		_, ok := err.(*ResourceCreationError)
		return ok
	case *ResourceDeletionError:
		_, ok := err.(*ResourceDeletionError)
		return ok
	case *ConfigurationError:
		_, ok := err.(*ConfigurationError)
		return ok
	case *TimeoutError:
		_, ok := err.(*TimeoutError)
		return ok
	case *QuotaExceededError:
		_, ok := err.(*QuotaExceededError)
		return ok
	case *NetworkError:
		_, ok := err.(*NetworkError)
		return ok
	case *ValidationError:
		_, ok := err.(*ValidationError)
		return ok
	default:
		return false
	}
}
