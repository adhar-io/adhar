package apps

import (
	"reflect"
	"testing"
)

func TestThePlatformRouteCarriesTheEnvironmentAndNoPort(t *testing.T) {
	if got := platformRouteHost("shop", "dev", "platform.adhar.io"); got != "shop.dev.platform.adhar.io" {
		t.Errorf("got %q", got)
	}
	if got := platformRouteHost("shop", "test", "adhar.localtest.me:8443"); got != "shop.test.adhar.localtest.me" {
		t.Errorf("port must be stripped: got %q", got)
	}
}

func TestCustomHostnamesAreAppendedAfterThePlatformRoute(t *testing.T) {
	got := routeHostnames("shop.dev.platform.adhar.io", "Shop.Example.org, https://www.example.org/, shop.example.org")
	want := []string{"shop.dev.platform.adhar.io", "shop.example.org", "www.example.org"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got %v, want %v", got, want)
	}
	if got := routeHostnames("a.dev.x.io", ""); !reflect.DeepEqual(got, []string{"a.dev.x.io"}) {
		t.Errorf("no custom hostnames: got %v", got)
	}
	if got := routeHostnamesJSON("a.dev.x.io", "b.x.io"); got != `["a.dev.x.io","b.x.io"]` {
		t.Errorf("JSON form: got %s", got)
	}
}

func TestAValueThatIsNotAHostnameIsDropped(t *testing.T) {
	got := parseCustomHostnames("not a host, -bad.example.org, ok.example.org, *.wild.example.org")
	want := []string{"ok.example.org", "*.wild.example.org"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got %v, want %v", got, want)
	}
}

func TestAnEnvironmentHostnameMovesBetweenEnvironments(t *testing.T) {
	if got := envHostname("shop.dev.platform.adhar.io", "dev", "prod"); got != "shop.prod.platform.adhar.io" {
		t.Errorf("got %q", got)
	}
	if got := envHostname("shop.adhar.localtest.me", "dev", "test"); got != "shop.adhar.localtest.me" {
		t.Errorf("a hostname without the environment label must be left alone, got %q", got)
	}
}

// A host named after an environment (test.adhar.io) cannot carry
// `<app>.test.test.adhar.io`: Let's Encrypt refuses the repeated label and the
// Gateway has no such listener. That environment's route moves under the host's
// own wildcard as `<app>-test.test.adhar.io`.
func TestAnEnvironmentThatRepeatsTheHostLabelRoutesUnderTheHostWildcard(t *testing.T) {
	if got := platformRouteHost("ngapp", "test", "test.adhar.io"); got != "ngapp-test.test.adhar.io" {
		t.Fatalf("platformRouteHost = %q, want ngapp-test.test.adhar.io", got)
	}
	if got := platformRouteHost("ngapp", "dev", "test.adhar.io"); got != "ngapp.dev.test.adhar.io" {
		t.Fatalf("a non-colliding environment changed: %q", got)
	}
	if got := platformRouteHost("ngapp", "test", "platform.adhar.io"); got != "ngapp.test.platform.adhar.io" {
		t.Fatalf("a neutral host changed: %q", got)
	}
}
