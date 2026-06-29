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

// Register exchanges the post-login authorization code for durable device
// credentials (the second phase of login). This hits Amazon and cannot be unit
// tested against the live service.
func Register(ctx context.Context, mp Marketplace, code string, pkce PKCE, serial string) (Credentials, error) {
	return register(ctx, "https://"+mp.AmazonAPIDomain(), mp, code, pkce, serial)
}

func register(ctx context.Context, baseURL string, mp Marketplace, code string, pkce PKCE, serial string) (Credentials, error) {
	body, err := json.Marshal(registerBody{
		RequestedTokenType: []string{"bearer", "mac_dms", "website_cookies", "store_authentication_cookie"},
		Cookies:            registerCookies{WebsiteCookies: []string{}, Domain: mp.CookieDomain()},
		RegistrationData: map[string]string{
			"domain":           "Device",
			"app_version":      "3.56.2",
			"device_serial":    serial,
			"device_type":      deviceType,
			"device_name":      "AudioSilo Manager",
			"os_version":       "17.0",
			"software_version": "35602678",
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
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, baseURL+"/auth/register", bytes.NewReader(body))
	if err != nil {
		return Credentials{}, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("x-amzn-identity-auth-domain", "api.amazon."+mp.TLD)

	resp, err := (&http.Client{Timeout: 30 * time.Second}).Do(req)
	if err != nil {
		return Credentials{}, err
	}
	defer func() { _ = resp.Body.Close() }()
	data, _ := io.ReadAll(resp.Body)
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return Credentials{}, fmt.Errorf("register: HTTP %d: %s", resp.StatusCode, snippet(data))
	}
	return parseRegister(data, mp, serial)
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
	}, nil
}
