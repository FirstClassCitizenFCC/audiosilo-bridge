package audible

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"net/url"
	"strings"
)

// deviceType is the Audible-for-iPhone device type both mkb79/audible and
// rmcrackan/AudibleApi register as.
const deviceType = "A2CZJZGLK2JJVM"

// NewDeviceSerial returns a random uppercase-hex device serial.
func NewDeviceSerial() (string, error) {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return strings.ToUpper(hex.EncodeToString(b)), nil
}

// clientID is the hex of "<serial>#<deviceType>", as Audible expects.
func clientID(serial string) string {
	return hex.EncodeToString([]byte(serial + "#" + deviceType))
}

// BuildAuthorizeURL builds the Amazon OAuth authorize URL the user opens in a
// browser. After signing in they are redirected to .../ap/maplanding?...&
// openid.oa2.authorization_code=...; pass that redirect URL to ParseAuthCode.
func BuildAuthorizeURL(mp Marketplace, pkce PKCE, serial string) string {
	returnTo := "https://" + mp.AmazonDomain() + "/ap/maplanding"
	q := url.Values{
		"openid.oa2.response_type":         {"code"},
		"openid.oa2.code_challenge_method": {"S256"},
		"openid.oa2.code_challenge":        {pkce.Challenge},
		"openid.oa2.client_id":             {"device:" + clientID(serial)},
		"openid.oa2.scope":                 {"device_auth_access"},
		"openid.return_to":                 {returnTo},
		"openid.assoc_handle":              {"amzn_audible_ios_" + mp.Code},
		"openid.identity":                  {"http://specs.openid.net/auth/2.0/identifier_select"},
		"openid.claimed_id":                {"http://specs.openid.net/auth/2.0/identifier_select"},
		"openid.mode":                      {"checkid_setup"},
		"openid.ns.oa2":                    {"http://www.amazon.com/ap/ext/oauth/2"},
		"openid.ns.pape":                   {"http://specs.openid.net/extensions/pape/1.0"},
		"openid.ns":                        {"http://specs.openid.net/auth/2.0"},
		"openid.pape.max_auth_age":         {"0"},
		"marketPlaceId":                    {mp.MarketPlaceID},
		"pageId":                           {"amzn_audible_ios"},
		"accountStatusPolicy":              {"P1"},
		"forceMobileLayout":                {"true"},
	}
	return "https://" + mp.AmazonDomain() + "/ap/signin?" + q.Encode()
}

// ParseAuthCode extracts openid.oa2.authorization_code from the post-login redirect
// URL (the /ap/maplanding URL the user is sent to / copies back).
func ParseAuthCode(redirectURL string) (string, error) {
	u, err := url.Parse(strings.TrimSpace(redirectURL))
	if err != nil {
		return "", fmt.Errorf("parse redirect URL: %w", err)
	}
	code := u.Query().Get("openid.oa2.authorization_code")
	if code == "" {
		return "", fmt.Errorf("no authorization code in the redirect URL (expected an .../ap/maplanding?...authorization_code=... link)")
	}
	return code, nil
}
