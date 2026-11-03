//go:build !audiblefake

package audible

import "testing"

// Denied: without the audiblefake tag (every release build), the fake's
// variable changes nothing and every endpoint is the marketplace's.
func TestNoFakeWithoutTag(t *testing.T) {
	t.Setenv("AUDIOSILO_BRIDGE_AUDIBLE_FAKE_URL", "http://127.0.0.1:8099")
	mp, _ := MarketplaceFor("us")
	if FakeURL() != "" {
		t.Fatal("FakeURL is set in an untagged build")
	}
	if e := EndpointsFor(mp); e.Amazon != "https://www.amazon.com" || e.AmazonAPI != "https://api.amazon.com" || e.API != "https://api.audible.com" {
		t.Fatalf("endpoints = %+v", e)
	}
}
