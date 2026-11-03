package audible

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// amazonRefusal is the shape of a real license refusal (edge jobs 594/595),
// with a made-up customer id.
const amazonRefusal = "Customer is not part of any plans; Customer id [A1B2C3D4E5F6G7] being throttled; " +
	"urn:cdo:AudibleApiExternalRouterService:Prod:Default[AAA], does not has access to asin[B00H3R0B0W]."

// Denied: every account identifier goes; the human reason and the ASIN stay.
func TestRedactAccountIDs(t *testing.T) {
	cases := []struct {
		name, in   string
		gone, kept []string
	}{
		{
			name: "license refusal",
			in:   amazonRefusal,
			gone: []string{"A1B2C3D4E5F6G7"},
			kept: []string{
				"Customer is not part of any plans", "Customer id [redacted] being throttled",
				"urn:cdo:AudibleApiExternalRouterService:Prod:Default[AAA]", "does not has access to asin[B00H3R0B0W].",
			},
		},
		{
			name: "amzn1 account id",
			in:   "no access for amzn1.account.AFAKEACCOUNTID123XYZ on B0HP4W2JK7",
			gone: []string{"amzn1.account", "AFAKEACCOUNTID123XYZ"},
			kept: []string{"no access for", "B0HP4W2JK7"},
		},
		{
			name: "directed id",
			in:   `{"directedId":"amzn1.actor.person.oid.A2FAKEFAKEFAKE"}`,
			gone: []string{"amzn1", "A2FAKEFAKEFAKE"},
		},
		{
			name: "labelled json customer id",
			in:   `{"customer_id": "A3XYZFAKE12345", "message": "throttled"}`,
			gone: []string{"A3XYZFAKE12345"},
			kept: []string{`"customer_id": "[redacted]"`, "throttled"},
		},
		{
			name: "email",
			in:   "account someone.name+audible@example.co.uk was refused",
			gone: []string{"someone.name", "example.co.uk"},
			kept: []string{"account [redacted] was refused"},
		},
		{
			name: "bearer",
			in:   "Authorization: Bearer Atna|EwICIFAKEtokenvalue.abc-123",
			gone: []string{"Atna|", "EwICIFAKEtokenvalue"},
			kept: []string{"Authorization:"},
		},
		{
			name: "amazon tokens and key=value",
			in:   `refresh Atzr|IwEBIFAKErefresh; access_token="sekrit-value"; adp_token: {enc:abc}`,
			gone: []string{"Atzr|", "IwEBIFAKErefresh", "sekrit-value", "enc:abc"},
			kept: []string{"refresh", "access_token="},
		},
		{
			name: "device serial",
			in:   "device serial 0123456789ABCDEF0123456789ABCDEF is not registered; device_serial_number=FAKESERIAL1",
			gone: []string{"0123456789ABCDEF0123456789ABCDEF", "FAKESERIAL1"},
			kept: []string{"is not registered"},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := RedactAccountIDs(tc.in)
			for _, g := range tc.gone {
				if strings.Contains(got, g) {
					t.Errorf("%q survived: %s", g, got)
				}
			}
			for _, k := range tc.kept {
				if !strings.Contains(got, k) {
					t.Errorf("%q lost: %s", k, got)
				}
			}
			if again := RedactAccountIDs(got); again != got {
				t.Errorf("redacting twice changed it:\n%s\n%s", got, again)
			}
		})
	}
}

// Allowed: text with no account identifier is unchanged: ASINs (B0..., 10
// digits, an ISBN-10 ending X), all-capital reasons, paths and HTTP statuses.
func TestRedactAccountIDsKeepsASINs(t *testing.T) {
	for _, s := range []string{
		"does not has access to asin[B00H3R0B0W]",
		"no license voucher returned for B0HP4W2JK7",
		"the title 1774247545 isn't released yet",
		"the title 080442957X isn't released yet",
		"POST /1.0/content/B00H3R0B0W/licenserequest: HTTP 403: ACCESSDENIED ContentEligibility Ownership",
		"Customer is not part of any plans",
		"the token expired; sign in again",
		"",
	} {
		if got := RedactAccountIDs(s); got != s {
			t.Errorf("changed %q to %q", s, got)
		}
	}
}

// Denied: a license refusal never carries the customer id, whether Audible
// sends it as a denial reason or as an error body.
func TestLicenseErrorsRedactTheCustomerID(t *testing.T) {
	denial := fmt.Sprintf(`{"content_license":{"status_code":"Denied","license_denial_reasons":[{"message":%q,"rejectionReason":"ContentEligibility","validationType":"Ownership"}]}}`, amazonRefusal)
	for name, h := range map[string]http.HandlerFunc{
		"denial reason": func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte(denial)) },
		"error body": func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusBadRequest)
			_, _ = fmt.Fprintf(w, `{"message":%q}`, amazonRefusal)
		},
	} {
		t.Run(name, func(t *testing.T) {
			ts := httptest.NewServer(h)
			defer ts.Close()
			_, err := testClient(t, ts.URL).License(context.Background(), "B00H3R0B0W")
			if err == nil {
				t.Fatal("expected an error")
			}
			msg := err.Error()
			if strings.Contains(msg, "A1B2C3D4E5F6G7") {
				t.Fatalf("customer id in the error: %s", msg)
			}
			if !strings.Contains(msg, "B00H3R0B0W") || !strings.Contains(msg, "being throttled") {
				t.Fatalf("ASIN or reason lost: %s", msg)
			}
		})
	}
}

// Denied: a long error body cut for the message never leaves part of an id.
func TestSnippetRedactsBeforeCutting(t *testing.T) {
	body := strings.Repeat("x", 190) + " Customer id [A1B2C3D4E5F6G7]"
	if got := snippet([]byte(body)); strings.Contains(got, "A1B2") {
		t.Fatalf("part of the id survived the cut: %s", got)
	}
}

// RedactedError redacts the text and keeps errors.Is/As working.
func TestRedactedError(t *testing.T) {
	if RedactedError(nil) != nil {
		t.Fatal("nil must stay nil")
	}
	err := RedactedError(fmt.Errorf("asking Audible for a license: %w: %s", ErrNoRights, amazonRefusal))
	if strings.Contains(err.Error(), "A1B2C3D4E5F6G7") || !strings.Contains(err.Error(), "B00H3R0B0W") {
		t.Fatalf("not redacted: %s", err)
	}
	if !errors.Is(err, ErrNoRights) {
		t.Fatal("errors.Is lost the cause")
	}
	if RedactedError(err) != err {
		t.Fatal("wrapping twice")
	}
}
