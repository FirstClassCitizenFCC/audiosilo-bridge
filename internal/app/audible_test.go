package app

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/kodestar/audiosilo-bridge/internal/api"
	"github.com/kodestar/audiosilo-bridge/internal/audible"
	"github.com/kodestar/audiosilo-bridge/internal/audibleacct"
	"github.com/kodestar/audiosilo-bridge/internal/link"
)

// registerErr is a real Register failure against an identity API answering
// status, wrapped as audibleacct.Finish wraps it.
func registerErr(t *testing.T, status int) error {
	t.Helper()
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(status)
		_, _ = w.Write([]byte(`{"response":{"error":{"code":"InvalidValue"}}}`))
	}))
	defer ts.Close()
	mp, _ := audible.MarketplaceFor("us")
	pkce, err := audible.NewPKCE()
	if err != nil {
		t.Fatal(err)
	}
	_, err = audible.Register(context.Background(), mp, "code", pkce, "SERIAL",
		audible.WithEndpoints(audible.Endpoints{Amazon: ts.URL, AmazonAPI: ts.URL, API: ts.URL}))
	if err == nil {
		t.Fatalf("a %d registered", status)
	}
	return fmt.Errorf("%w: %w", audibleacct.ErrRegister, err)
}

// A refusal (Amazon's or the server's 4xx) is a 422 with its answer; Amazon or
// the server failing or not answering is a 502 with a fixed sentence, never the
// transport's text.
func TestAudibleErrorKinds(t *testing.T) {
	t.Parallel()
	transport := &url.Error{Op: "Post", URL: "https://api.amazon.com/auth/register", Err: errors.New("dial tcp: connection refused")}
	cases := []struct {
		name string
		err  error
		kind error
		want string
	}{
		{"amazon refuses the code", registerErr(t, http.StatusBadRequest), api.ErrRefused, "wasn't accepted"},
		{"amazon fails", registerErr(t, http.StatusInternalServerError), api.ErrUnreachable, "Amazon couldn't take the sign-in"},
		{"amazon unreachable", fmt.Errorf("%w: %w", audibleacct.ErrRegister, transport), api.ErrUnreachable, "Amazon couldn't take the sign-in"},
		{"server refuses", &link.APIError{Status: 404, Code: "unknown_user", Message: "no such user"}, api.ErrRefused, "the server refused the account: no such user"},
		{"server fails", &link.APIError{Status: 503, Message: "Service Unavailable"}, api.ErrUnreachable, "HTTP 503"},
		{"server unreachable", &url.Error{Op: "Put", URL: "http://srv/api", Err: errors.New("EOF")}, api.ErrUnreachable, "couldn't be reached"},
		{"bridge token refused", link.ErrUnauthorized, api.ErrRefused, ""},
		{"expired", audibleacct.ErrLoginExpired, api.ErrNotFound, "expired"},
		{"not paired", audibleacct.ErrNotPaired, api.ErrConflict, "pair"},
	}
	for _, c := range cases {
		got := audibleError(c.err)
		if !errors.Is(got, c.kind) || !strings.Contains(got.Error(), c.want) {
			t.Errorf("%s: %v, want %v holding %q", c.name, got, c.kind, c.want)
		}
		if c.kind == api.ErrUnreachable && (strings.Contains(got.Error(), "dial tcp") || strings.Contains(got.Error(), "EOF")) {
			t.Errorf("%s: the transport's text reached the page: %v", c.name, got)
		}
	}
	if audibleError(nil) != nil {
		t.Fatal("nil is an error")
	}
	local := errors.New("disk full")
	if got := audibleError(local); got != local {
		t.Fatalf("a local failure = %v, want it as it is (a logged 500)", got)
	}
}
