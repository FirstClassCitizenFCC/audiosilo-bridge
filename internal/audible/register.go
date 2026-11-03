package audible

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"time"
)

type registerBody struct {
	RequestedTokenType  []string          `json:"requested_token_type"`
	Cookies             registerCookies   `json:"cookies"`
	RegistrationData    map[string]string `json:"registration_data"`
	AuthData            map[string]string `json:"auth_data"`
	RequestedExtensions []string          `json:"requested_extensions"`
}

// registerCookies is required by /auth/register whenever website/store cookie
// token types are requested; omitting it makes Amazon reject with MissingValue.
// website_cookies must serialize as [] (not null), so the slice is non-nil.
type registerCookies struct {
	WebsiteCookies []string `json:"website_cookies"`
	Domain         string   `json:"domain"`
}

type registerResp struct {
	Response struct {
		Success struct {
			Tokens struct {
				Bearer struct {
					AccessToken  string `json:"access_token"`
					RefreshToken string `json:"refresh_token"`
					ExpiresIn    string `json:"expires_in"`
				} `json:"bearer"`
				MacDMS struct {
					ADPToken         string `json:"adp_token"`
					DevicePrivateKey string `json:"device_private_key"`
				} `json:"mac_dms"`
			} `json:"tokens"`
			Extensions struct {
				DeviceInfo struct {
					DeviceSerialNumber string `json:"device_serial_number"`
				} `json:"device_info"`
				CustomerInfo struct {
					UserID string `json:"user_id"`
				} `json:"customer_info"`
			} `json:"extensions"`
		} `json:"success"`
	} `json:"response"`
}

// DeviceName is the name the registered device carries in the Amazon account's
// device list, after the account holder's first name ("Chris's AudioSilo").
const DeviceName = "AudioSilo"

// Register exchanges the post-login authorization code for durable device
// credentials (the second phase of login). serial is the one the authorize URL
// was built with.
func Register(ctx context.Context, mp Marketplace, code string, pkce PKCE, serial string, opts ...Option) (Credentials, error) {
	set := resolve(mp, authTimeout, opts)
	body, err := json.Marshal(registerBody{
		RequestedTokenType: []string{"bearer", "mac_dms", "website_cookies", "store_authentication_cookie"},
		Cookies:            registerCookies{WebsiteCookies: []string{}, Domain: mp.CookieDomain()},
		RegistrationData: map[string]string{
			"domain":        "Device",
			"app_version":   appVersion,
			"device_serial": serial,
			"device_type":   deviceType,
			// Amazon rejects a second device with an identical name (DuplicateDeviceName).
			// The %DUPE_STRATEGY_1ST% macro tells the registration service to de-dupe
			// server-side (the %FIRST_NAME% macros expand to the account holder's name),
			// so retries don't collide - this is the mkb79/audible-proven form.
			"device_name":      "%FIRST_NAME%%FIRST_NAME_POSSESSIVE_STRING%%DUPE_STRATEGY_1ST%" + DeviceName,
			"os_version":       osVersion,
			"software_version": softwareVersion,
			"device_model":     "iPhone",
			"app_name":         "Audible",
		},
		AuthData: map[string]string{
			"client_id":          clientID(serial),
			"authorization_code": code,
			"code_verifier":      pkce.Verifier,
			"code_algorithm":     "SHA-256",
			"client_domain":      "DeviceLegacy",
		},
		RequestedExtensions: []string{"device_info", "customer_info"},
	})
	if err != nil {
		return Credentials{}, err
	}
	data, err := postAuth(ctx, set, "/auth/register", mp, "application/json", body, "")
	if err != nil {
		return Credentials{}, err
	}
	return parseRegister(data, mp, serial)
}

// authTimeout bounds one identity call (register, refresh, deregister).
const authTimeout = 30 * time.Second

// postAuth POSTs body to the identity API's path (with a bearer token when
// accessToken is set) under the profile's user agent, backing off on 429/503,
// and returns the 2xx answer's body.
// An error never carries the request: its body holds the code, the verifier or
// a token.
func postAuth(ctx context.Context, set settings, path string, mp Marketplace, contentType string, body []byte, accessToken string) ([]byte, error) {
	resp, err := sendWithBackoff(ctx, set.http, set.sleep, func() (*http.Request, error) {
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, set.endpoints.AmazonAPI+path, bytes.NewReader(body))
		if err != nil {
			return nil, err
		}
		req.Header.Set("Content-Type", contentType)
		req.Header.Set("Accept", "application/json")
		// The profile's own user agent, as on every signed call: Go's default
		// one names no app the device registered as.
		req.Header.Set("User-Agent", userAgent)
		req.Header.Set("x-amzn-identity-auth-domain", mp.AmazonAPIDomain())
		if accessToken != "" {
			req.Header.Set("Authorization", "Bearer "+accessToken)
		}
		return req, nil
	})
	if err != nil {
		return nil, err
	}
	defer func() { _ = resp.Body.Close() }()
	data, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, &httpError{StatusCode: resp.StatusCode, Method: http.MethodPost, Path: path, Body: snippet(data)}
	}
	return data, nil
}

func parseRegister(data []byte, mp Marketplace, serial string) (Credentials, error) {
	var r registerResp
	if err := json.Unmarshal(data, &r); err != nil {
		return Credentials{}, fmt.Errorf("register response: %w", err)
	}
	s := r.Response.Success
	if s.Tokens.MacDMS.ADPToken == "" || s.Tokens.MacDMS.DevicePrivateKey == "" {
		return Credentials{}, fmt.Errorf("register response missing device credentials")
	}
	deviceSerial := s.Extensions.DeviceInfo.DeviceSerialNumber
	if deviceSerial == "" {
		deviceSerial = serial
	}
	expires := int64(0)
	if n, err := strconv.Atoi(s.Tokens.Bearer.ExpiresIn); err == nil {
		expires = time.Now().Add(time.Duration(n) * time.Second).Unix()
	}
	return Credentials{
		AccessToken:      s.Tokens.Bearer.AccessToken,
		RefreshToken:     s.Tokens.Bearer.RefreshToken,
		ADPToken:         s.Tokens.MacDMS.ADPToken,
		DevicePrivateKey: s.Tokens.MacDMS.DevicePrivateKey,
		DeviceType:       deviceType,
		DeviceSerial:     deviceSerial,
		CustomerID:       s.Extensions.CustomerInfo.UserID,
		Marketplace:      mp.Code,
		ExpiresAt:        expires,
		Profile:          ProfileVersion,
	}, nil
}
