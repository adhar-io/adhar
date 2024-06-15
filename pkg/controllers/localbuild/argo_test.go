package localbuild

import (
	"testing"

	"github.com/adhar-io/adhar/pkg/k8s"
	"github.com/adhar-io/adhar/pkg/util"
)

func TestGetRawInstallResources(t *testing.T) {
	e := EmbeddedInstallation{
		resourceFS:   installArgoFS,
		resourcePath: "resources/argo",
	}
	resources, err := util.ConvertFSToBytes(e.resourceFS, e.resourcePath,
		util.CorePackageTemplateConfig{
			Protocol:       "",
			Host:           "",
			Port:           "",
			UsePathRouting: false,
		},
	)
	if err != nil {
		t.Fatalf("GetRawInstallResources() error: %v", err)
	}
	if len(resources) != 2 {
		t.Fatalf("GetRawInstallResources() resources len != 2, got %d", len(resources))
	}

	resourcePrefix := "# UCP ARGO INSTALL RESOURCES\n"
	checkPrefix := resources[1][0:len(resourcePrefix)]
	if resourcePrefix != string(checkPrefix) {
		t.Fatalf("GetRawInstallResources() expected 1 resource with prefix %q, got %q", resourcePrefix, checkPrefix)
	}
}

func TestGetK8sInstallResources(t *testing.T) {
	e := EmbeddedInstallation{
		resourceFS:   installArgoFS,
		resourcePath: "resources/argo",
	}
	objs, err := e.installResources(k8s.GetScheme(), util.CorePackageTemplateConfig{
		Protocol:       "",
		Host:           "",
		Port:           "",
		UsePathRouting: false,
	})
	if err != nil {
		t.Fatalf("GetK8sInstallResources() error: %v", err)
	}

	if len(objs) != 58 {
		t.Fatalf("Expected 58 Argo Install Resources, got: %d", len(objs))
	}
}
