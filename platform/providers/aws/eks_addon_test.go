package aws

import (
	"context"
	"errors"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/eks"
	ekstypes "github.com/aws/aws-sdk-go-v2/service/eks/types"
)

type fakeEKSAddons struct {
	calls []*eks.CreateAddonInput
	// rejectConfig makes EKS refuse any call that carries configuration values.
	rejectConfig bool
	err          error
}

func (f *fakeEKSAddons) CreateAddon(_ context.Context, in *eks.CreateAddonInput, _ ...func(*eks.Options)) (*eks.CreateAddonOutput, error) {
	f.calls = append(f.calls, in)
	if f.rejectConfig && in.ConfigurationValues != nil {
		return nil, &ekstypes.InvalidParameterException{Message: aws.String("ConfigurationValue provided in request is not supported")}
	}
	return &eks.CreateAddonOutput{}, f.err
}

// The EBS CSI addon carries the install id; and if EKS rejects that
// configuration, the addon is installed WITHOUT it rather than not at all — a
// cluster with no CSI driver cannot bind a single claim.
func TestTheEBSAddonFallsBackWhenItsConfigurationIsRejected(t *testing.T) {
	ok := &fakeEKSAddons{}
	if err := createEBSCSIAddon(context.Background(), ok, "team", "a1b2c3d4e5f6g7h8"); err != nil {
		t.Fatal(err)
	}
	if len(ok.calls) != 1 || aws.ToString(ok.calls[0].ConfigurationValues) != eksEBSAddonConfiguration("a1b2c3d4e5f6g7h8") {
		t.Fatalf("calls = %+v; want one, carrying the install id", ok.calls)
	}

	rejecting := &fakeEKSAddons{rejectConfig: true}
	if err := createEBSCSIAddon(context.Background(), rejecting, "team", "a1b2c3d4e5f6g7h8"); err != nil {
		t.Fatalf("the fallback did not install the addon: %v", err)
	}
	if len(rejecting.calls) != 2 || rejecting.calls[1].ConfigurationValues != nil {
		t.Fatalf("calls = %d; want a second, unconfigured attempt", len(rejecting.calls))
	}

	inUse := &fakeEKSAddons{err: &ekstypes.ResourceInUseException{Message: aws.String("exists")}}
	if err := createEBSCSIAddon(context.Background(), inUse, "team", ""); err != nil {
		t.Fatalf("an addon that already exists is not a failure: %v", err)
	}
	broken := &fakeEKSAddons{err: errors.New("AccessDenied")}
	if err := createEBSCSIAddon(context.Background(), broken, "team", "a1b2c3d4e5f6g7h8"); err == nil || len(broken.calls) != 1 {
		t.Fatalf("an unrelated failure was retried or swallowed: %v, %d calls", err, len(broken.calls))
	}
}
