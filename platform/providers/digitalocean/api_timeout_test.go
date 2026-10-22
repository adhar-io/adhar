package digitalocean

import "testing"

// The DigitalOcean client must bound each request itself: the node
// autoscaler calls it on a context with no deadline, so an unanswered request
// would otherwise stop autoscaling until the manager restarts.
func TestTheDigitalOceanClientBoundsEveryRequest(t *testing.T) {
	p, err := NewProvider(&Config{Token: "dop_v1_test", Region: "blr1"})
	if err != nil {
		t.Fatal(err)
	}
	if p.client == nil || p.client.HTTPClient == nil {
		t.Fatal("no HTTP client")
	}
	if p.client.HTTPClient.Timeout <= 0 {
		t.Fatal("DigitalOcean API requests have no timeout")
	}
}
