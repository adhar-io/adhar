package v1alpha1

import (
	"reflect"
	"testing"
)

// A host named after an environment would produce `*.test.test.<zone>`, which
// Let's Encrypt refuses and which then fails the whole certificate.
func TestRouteEnvironmentsSkipAnEnvironmentThatRepeatsTheHostsFirstLabel(t *testing.T) {
	all := BuildCustomizationSpec{Host: "platform.adhar.io"}.RouteEnvironments()
	if !reflect.DeepEqual(all, []string{"dev", "test", "prod"}) {
		t.Fatalf("a neutral host lost environments: %v", all)
	}
	got := BuildCustomizationSpec{Host: "test.adhar.io"}.RouteEnvironments()
	if !reflect.DeepEqual(got, []string{"dev", "prod"}) {
		t.Fatalf("host test.adhar.io must drop the test listener (*.test.test.adhar.io is refused by Let's Encrypt), got %v", got)
	}
	if skipped := (BuildCustomizationSpec{Host: "test.adhar.io"}).SkippedRouteEnvironments(); !reflect.DeepEqual(skipped, []string{"test"}) {
		t.Fatalf("the skipped environment is not reported: %v", skipped)
	}
	if got := (BuildCustomizationSpec{Host: "Dev.example.org"}).RouteEnvironments(); !reflect.DeepEqual(got, []string{"test", "prod"}) {
		t.Fatalf("the comparison must ignore case: %v", got)
	}
}
