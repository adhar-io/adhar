package custom

import (
	"strings"
	"testing"
)

func TestCloudIntegrationStepsInstallLocalPathDefaultClass(t *testing.T) {
	p := &Provider{config: &Config{SSHUser: "root"}}
	joined := ""
	for _, s := range p.cloudIntegrationSteps() {
		joined += s.Cmd + "\n"
	}
	for _, want := range []string{"apply -f '" + localPathProvisionerURL + "'", "patch storageclass local-path", "is-default-class"} {
		if !strings.Contains(joined, want) {
			t.Errorf("on-prem integration lacks %q", want)
		}
	}
}
