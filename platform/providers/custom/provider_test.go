package custom

import (
	"context"
	"strings"
	"testing"
)

func TestScaleNodeGroupGuardsBeforeTouchingAnyHost(t *testing.T) {
	p := &Provider{config: &Config{MasterIPs: []string{"10.0.0.1"}, WorkerIPs: []string{"10.0.0.2", "10.0.0.3"}, SSHUser: "root", SSHKeyPath: "/nonexistent/key"}}
	if err := p.ScaleNodeGroup(context.Background(), "custom-dev", "gpu", 1); err == nil || !strings.Contains(err.Error(), "workers") {
		t.Errorf("only the workers group exists: %v", err)
	}
	if err := p.ScaleNodeGroup(context.Background(), "custom-dev", "workers", 3); err == nil || !strings.Contains(err.Error(), "workerIPs lists 2") {
		t.Errorf("scaling past the configured hosts must say how to fix it: %v", err)
	}
	if err := p.ScaleNodeGroup(context.Background(), "custom-dev", "workers", -1); err == nil {
		t.Error("negative replicas are rejected")
	}
}

func TestGetNodeGroupReportsTheConfiguredWorkersWithoutSSH(t *testing.T) {
	p := &Provider{config: &Config{MasterIPs: []string{"10.0.0.1"}, WorkerIPs: []string{"10.0.0.2", "10.0.0.3"}, SSHUser: "root", SSHKeyPath: "/nonexistent/key"}}
	g, err := p.GetNodeGroup(context.Background(), "custom-dev", "workers")
	if err != nil || g.Replicas != 2 || g.InstanceType != "byo-host" {
		t.Errorf("unexpected %+v %v", g, err)
	}
	if _, err := p.GetNodeGroup(context.Background(), "custom-dev", "other"); err == nil {
		t.Error("unknown group is an error")
	}
	groups, err := p.ListNodeGroups(context.Background(), "custom-dev")
	if err != nil || len(groups) != 1 || groups[0].Name != "workers" {
		t.Errorf("unexpected %+v %v", groups, err)
	}
}
