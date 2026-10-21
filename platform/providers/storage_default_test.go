package provider

import (
	"encoding/json"
	"strings"
	"testing"

	"adhar-io/adhar/globals"
)

// The platform's default StorageClass must NOT be the cloud's block class.
// A block volume costs one of the VM's data-disk attach slots — 8 on an Azure
// Standard_E4bds_v5, 7 on a DigitalOcean droplet — while the enabled packages
// ask for roughly ninety PersistentVolumeClaims. A four-worker cluster
// therefore runs out of attach slots at ~32 volumes and leaves every further
// stateful pod Pending on "node(s) exceed max volume count" with the nodes
// under half their CPU. That is exactly the wall a live Azure bring-up hit at
// 39 of 75 apps.
func TestBlockStorageClassIsNotTheDefault(t *testing.T) {
	step := StepStorageClass(globals.BlockStorageClass, "disk.csi.azure.com", map[string]string{"skuName": "StandardSSD_LRS"}, false)
	if !strings.Contains(step.Cmd, `storageclass.kubernetes.io/is-default-class: "false"`) {
		t.Fatalf("block StorageClass must not be annotated as the default:\n%s", step.Cmd)
	}
	if !strings.Contains(step.Cmd, "skuName") {
		t.Fatalf("parameters lost:\n%s", step.Cmd)
	}
}

func TestStepDefaultStorageClassStillMarksTheDefault(t *testing.T) {
	step := StepDefaultStorageClass("x", "p", nil)
	if !strings.Contains(step.Cmd, `storageclass.kubernetes.io/is-default-class: "true"`) {
		t.Fatalf("StepDefaultStorageClass no longer marks the default:\n%s", step.Cmd)
	}
	if strings.Contains(step.Cmd, "parameters:") {
		t.Fatalf("an empty parameter map must not emit an empty parameters block:\n%s", step.Cmd)
	}
}

// The node-local class is what removes the attach ceiling, so it must be the
// default and must be provisioned by local-path — not by any CSI driver that
// attaches cloud disks.
func TestNodeLocalStorageClassIsTheDefaultAndHasNoAttachLimit(t *testing.T) {
	steps := StepNodeLocalStorageClass(globals.DefaultStorageClass)
	// Exactly one step, and it must not block on readiness. Integration runs
	// right after the first kubeadm joins — before Cilium — so nothing on pod
	// networking can be Available yet; a `rollout status` here failed the whole
	// `adhar up` after five minutes on a live cluster.
	if len(steps) != 1 {
		t.Fatalf("want a single apply step, got %d", len(steps))
	}
	if strings.Contains(steps[0].Cmd, "rollout status") || strings.Contains(steps[0].Cmd, "wait --for") {
		t.Errorf("must not wait for readiness before the CNI exists: %s", steps[0].Cmd)
	}
	m := steps[0].Cmd
	for _, want := range []string{
		"name: " + globals.DefaultStorageClass,
		`storageclass.kubernetes.io/is-default-class: "true"`,
		"provisioner: rancher.io/local-path",
		"volumeBindingMode: WaitForFirstConsumer",
		"rancher/local-path-provisioner:" + LocalPathProvisionerVersion,
		LocalPathDataDir,
	} {
		if !strings.Contains(m, want) {
			t.Errorf("node-local StorageClass manifest is missing %q", want)
		}
	}
	if strings.Contains(m, "csi.azure.com") || strings.Contains(m, "ebs.csi.aws.com") || strings.Contains(m, "pd.csi.storage.gke.io") {
		t.Error("the node-local class must not be backed by a cloud CSI driver — that reintroduces the attach limit")
	}
	// local-path cannot grow a volume in place; claiming otherwise would make
	// `kubectl patch pvc` look like a fix and silently do nothing.
	if strings.Contains(m, "allowVolumeExpansion: true") {
		t.Error("local-path does not support volume expansion; do not advertise it")
	}
	// A freshly joined node still carries the cloud-provider and CSI startup
	// taints, and the helper pod is pinned to that node: without a blanket
	// toleration the very first volume on a new node never gets created.
	if !strings.Contains(m, "- operator: Exists") {
		t.Error("the helper pod must tolerate a new node's startup taints")
	}
}

func TestStepClearDefaultStorageClassToleratesAMissingClass(t *testing.T) {
	cmd := StepClearDefaultStorageClass("civo-volume").Cmd
	if !strings.Contains(cmd, `"false"`) {
		t.Errorf("must set the annotation to false: %s", cmd)
	}
	// `adhar up` re-runs integration against clusters where the class may not
	// exist yet; the step must not fail the whole bring-up.
	if !strings.Contains(cmd, "|| true") {
		t.Errorf("must be tolerant of a class that is not installed: %s", cmd)
	}
}

// The helper pod that creates a node-local volume is a one-line mkdir, but the
// node it runs on is the busiest in the cluster during a bring-up. At the
// provisioner's 120 s default every attempt timed out on GCP and was torn down
// and retried, and keycloak-db's volume stayed Pending for 17+ minutes
// (2026-10-21).
func TestNodeLocalVolumeHelperGetsTimeOnABusyNode(t *testing.T) {
	m := NodeLocalStorageManifest("adhar-local")
	start := strings.Index(m, "config.json: |\n")
	if start < 0 {
		t.Fatal("local-path-config carries no config.json")
	}
	var body []string
	for _, line := range strings.Split(m[start:], "\n")[1:] {
		if !strings.HasPrefix(line, "    ") {
			break
		}
		body = append(body, line)
	}
	var cfg struct {
		CmdTimeoutSeconds int `json:"cmdTimeoutSeconds"`
		NodePathMap       []struct {
			Node string `json:"node"`
		} `json:"nodePathMap"`
	}
	if err := json.Unmarshal([]byte(strings.Join(body, "\n")), &cfg); err != nil {
		t.Fatalf("config.json does not parse (the provisioner would not start): %v", err)
	}
	if cfg.CmdTimeoutSeconds < 300 {
		t.Errorf("cmdTimeoutSeconds = %d; a helper pod on a loaded node needs minutes, not the 120 s default", cfg.CmdTimeoutSeconds)
	}
	if len(cfg.NodePathMap) == 0 {
		t.Error("config.json lost its nodePathMap")
	}
}
