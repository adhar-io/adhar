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
