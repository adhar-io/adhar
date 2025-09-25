package helpers

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json" // Added import
	"fmt"           // Added import
	"log"
	"time" // Added import

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1" // Added import
	"sigs.k8s.io/yaml"                            // Added import
)

// GenerateRandomString generates a random string of the specified length
func GenerateRandomString(length int) string {
	bytes := make([]byte, length)
	if _, err := rand.Read(bytes); err != nil {
		log.Fatal(err)
	}
	return hex.EncodeToString(bytes)[:length]
}

// PrintResource (placeholder) prints a Kubernetes resource in the specified format.
func PrintResource(resource interface{}, format string) error {
	switch format {
	case "json":
		data, err := json.MarshalIndent(resource, "", "  ")
		if err != nil {
			return fmt.Errorf("failed to marshal resource to JSON: %w", err)
		}
		fmt.Println(string(data))
	case "yaml":
		data, err := yaml.Marshal(resource)
		if err != nil {
			return fmt.Errorf("failed to marshal resource to YAML: %w", err)
		}
		fmt.Println(string(data))
	case "table":
		// Basic table output - might need customization based on resource type
		fmt.Printf("Printing resource (table format placeholder): %+v\n", resource)
	default:
		return fmt.Errorf("unsupported output format: %s", format)
	}
	return nil
}

// FormatAge (placeholder) formats a Kubernetes timestamp into a human-readable age string.
func FormatAge(timestamp metav1.Time) string {
	if timestamp.IsZero() {
		return "<unknown>"
	}
	return time.Since(timestamp.Time).Round(time.Second).String()
}

// PrintJSON prints an object as formatted JSON
func PrintJSON(obj interface{}) error {
	data, err := json.MarshalIndent(obj, "", "  ")
	if err != nil {
		return fmt.Errorf("failed to marshal to JSON: %w", err)
	}
	fmt.Println(string(data))
	return nil
}

// PrintYAML prints an object as formatted YAML
func PrintYAML(obj interface{}) error {
	data, err := yaml.Marshal(obj)
	if err != nil {
		return fmt.Errorf("failed to marshal to YAML: %w", err)
	}
	fmt.Println(string(data))
	return nil
}
