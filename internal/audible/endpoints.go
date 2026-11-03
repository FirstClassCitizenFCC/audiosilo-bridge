package audible

import (
	"context"
	"net/http"
	"strings"
	"time"
)

// Endpoints are the three hosts the client talks to, as base URLs with a scheme
// and no trailing slash. EndpointsFor gives a marketplace's real ones; a test, or
// a build with the audiblefake tag, points all three at a fake.
type Endpoints struct {
	// Amazon serves the sign-in page and the maplanding redirect
	// (https://www.amazon.<tld>).
	Amazon string
	// AmazonAPI is the identity API: register, token refresh, deregister
	// (https://api.amazon.<tld>).
	AmazonAPI string
	// API is the Audible API: library, license, annotations
	// (https://api.audible.<tld>).
	API string
}

// EndpointsFor returns the marketplace's endpoints. In a build with the
// audiblefake tag and AUDIOSILO_BRIDGE_AUDIBLE_FAKE_URL set, every endpoint is
// that URL instead (release builds never carry the tag, so the variable does
// nothing there).
func EndpointsFor(mp Marketplace) Endpoints {
	if u := FakeURL(); u != "" {
		return Endpoints{Amazon: u, AmazonAPI: u, API: u}
	}
	return Endpoints{
		Amazon:    "https://" + mp.AmazonDomain(),
		AmazonAPI: "https://" + mp.AmazonAPIDomain(),
		API:       "https://" + mp.APIDomain(),
	}
}

// Option adjusts a Client, or one call of Register, BuildAuthorizeURL,
// RefreshAccessToken or Deregister.
type Option func(*settings)

// WithEndpoints points the calls at e instead of the marketplace's hosts.
func WithEndpoints(e Endpoints) Option {
	return func(s *settings) {
		e.Amazon = strings.TrimRight(e.Amazon, "/")
		e.AmazonAPI = strings.TrimRight(e.AmazonAPI, "/")
		e.API = strings.TrimRight(e.API, "/")
		s.endpoints = &e
	}
}

// WithHTTPClient uses h for the API calls (its Timeout is kept). A download
// uses h's Transport with no overall timeout.
func WithHTTPClient(h *http.Client) Option {
	return func(s *settings) { s.http = h }
}

// withSleep replaces the backoff's wait (tests).
func withSleep(f func(context.Context, time.Duration) error) Option {
	return func(s *settings) { s.sleep = f }
}

// settings is what the options set, resolved against a marketplace.
type settings struct {
	endpoints *Endpoints
	http      *http.Client
	sleep     func(context.Context, time.Duration) error
}

// endpointsOf is the endpoints opts set, else mp's.
func endpointsOf(mp Marketplace, opts []Option) Endpoints {
	var s settings
	for _, o := range opts {
		o(&s)
	}
	if s.endpoints != nil {
		return *s.endpoints
	}
	return EndpointsFor(mp)
}

// resolve applies opts over the defaults for mp.
func resolve(mp Marketplace, timeout time.Duration, opts []Option) settings {
	var s settings
	for _, o := range opts {
		o(&s)
	}
	if s.endpoints == nil {
		e := EndpointsFor(mp)
		s.endpoints = &e
	}
	if s.http == nil {
		s.http = &http.Client{Timeout: timeout, Transport: wrapTransport(http.DefaultTransport)}
	}
	if s.sleep == nil {
		s.sleep = sleepCtx
	}
	return s
}

// sleepCtx waits d, or until ctx ends.
func sleepCtx(ctx context.Context, d time.Duration) error {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
}
