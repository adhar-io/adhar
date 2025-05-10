package k8s

import (
	"fmt"
	"os/exec"
)

// ApplyKCLManifest applies a KCL manifest to the Kubernetes cluster
func ApplyKCLManifest(manifestPath string) error {
	cmd := exec.Command("kubectl", "apply", "-f", manifestPath)
	output, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("failed to apply KCL manifest: %w\n%s", err, string(output))
	}
	return nil
}
