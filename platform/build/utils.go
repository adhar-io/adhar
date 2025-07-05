package build

import (
	"os"
	"os/exec"
	"strconv"
	"strings"
)

// parseIntOrDefault parses an integer string or returns a default value
func parseIntOrDefault(s string, defaultValue int) int {
	if val, err := strconv.Atoi(s); err == nil {
		return val
	}
	return defaultValue
}

// parseStringOrDefault returns the value or a default if empty
func parseStringOrDefault(s string, defaultValue string) string {
	if s != "" {
		return s
	}
	return defaultValue
}

// parseBoolOrDefault parses a boolean string or returns a default value
func parseBoolOrDefault(s string, defaultValue bool) bool {
	if val, err := strconv.ParseBool(s); err == nil {
		return val
	}
	return defaultValue
}

// runCommand executes a shell command
func runCommand(command string) error {
	parts := strings.Fields(command)
	if len(parts) == 0 {
		return nil
	}

	cmd := exec.Command(parts[0], parts[1:]...)
	return cmd.Run()
}

// writeStringToFile writes a string to a file
func writeStringToFile(filename, content string) error {
	return os.WriteFile(filename, []byte(content), 0644)
}
