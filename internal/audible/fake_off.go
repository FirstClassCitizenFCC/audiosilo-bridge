//go:build !audiblefake

package audible

import "net/http"

// FakeURL is "" in a build without the audiblefake tag: release builds always
// talk to Amazon and Audible. See fake_on.go.
func FakeURL() string { return "" }

// wrapTransport is rt itself without the audiblefake tag (no recording).
func wrapTransport(rt http.RoundTripper) http.RoundTripper { return rt }
