package gcp

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"cloud.google.com/go/compute/apiv1/computepb"
	"google.golang.org/api/googleapi"
	"google.golang.org/api/iterator"
	"google.golang.org/protobuf/proto"
)

func asGoogleAPIError(err error, target **googleapi.Error) bool {
	return errors.As(err, target)
}

// listFirewallRulesWithPrefix returns the project's firewall rules whose name
// starts with prefix.
func (p *Provider) listFirewallRulesWithPrefix(ctx context.Context, prefix string) []string {
	var rules []string
	it := p.firewallClient.List(ctx, &computepb.ListFirewallsRequest{
		Project: p.config.ProjectID,
		Filter:  proto.String(fmt.Sprintf("name eq %s.*", prefix)),
	})
	for {
		fw, err := it.Next()
		if err == iterator.Done || err != nil {
			break
		}
		if name := fw.GetName(); strings.HasPrefix(name, prefix) {
			rules = append(rules, name)
		}
	}
	return rules
}

// instanceIPs looks an instance's private and public addresses up.
func (p *Provider) instanceIPs(ctx context.Context, instanceName, zone string) (privateIP, publicIP string, err error) {
	inst, err := p.instanceClient.Get(ctx, &computepb.GetInstanceRequest{Project: p.config.ProjectID, Zone: zone, Instance: instanceName})
	if err != nil {
		return "", "", err
	}
	if len(inst.NetworkInterfaces) > 0 {
		privateIP = inst.NetworkInterfaces[0].GetNetworkIP()
		if len(inst.NetworkInterfaces[0].AccessConfigs) > 0 {
			publicIP = inst.NetworkInterfaces[0].AccessConfigs[0].GetNatIP()
		}
	}
	return privateIP, publicIP, nil
}
