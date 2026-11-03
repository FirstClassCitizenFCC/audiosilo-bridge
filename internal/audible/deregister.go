package audible

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"time"
)

// RefreshAccessToken trades the device's refresh token for a new access token
// (POST /auth/token on the identity API) and returns the credentials with it and
// its expiry set. The signed API calls never need one (the device key signs
// them); deregistering does.
func RefreshAccessToken(ctx context.Context, creds Credentials, opts ...Option) (Credentials, error) {
	mp, ok := MarketplaceFor(creds.Marketplace)
	if !ok {
		return Credentials{}, fmt.Errorf("unknown marketplace %q", creds.Marketplace)
	}
	if creds.RefreshToken == "" {
		return Credentials{}, errors.New("the credentials hold no refresh token")
	}
	set := resolve(mp, authTimeout, opts)
	form := url.Values{
		"app_name":             {"Audible"},
		"app_version":          {appVersion},
		"source_token":         {creds.RefreshToken},
		"requested_token_type": {"access_token"},
		"source_token_type":    {"refresh_token"},
	}
	data, err := postAuth(ctx, set, "/auth/token", mp, "application/x-www-form-urlencoded", []byte(form.Encode()), "")
	if err != nil {
		return Credentials{}, err
	}
	var r struct {
		AccessToken string `json:"access_token"`
		ExpiresIn   int64  `json:"expires_in"`
	}
	if err := json.Unmarshal(data, &r); err != nil {
		return Credentials{}, fmt.Errorf("token refresh response: %w", err)
	}
	if r.AccessToken == "" {
		return Credentials{}, errors.New("token refresh returned no access token")
	}
	creds.AccessToken = r.AccessToken
	creds.ExpiresAt = time.Now().Add(time.Duration(r.ExpiresIn) * time.Second).Unix()
	return creds, nil
}

// accessTokenMargin is how long before its expiry an access token is refreshed
// rather than sent.
const accessTokenMargin = 2 * time.Minute

// Deregister removes this device from the Amazon account (POST /auth/deregister
// with the device's bearer token), so a removed account leaves no "AudioSilo"
// device behind. The access token is refreshed first when it has expired or is
// about to. Only this device goes, never the account's others. The caller
// forgets the credentials locally FIRST (as the GitHub sign-out does), so a
// failure here leaves nothing usable on the Bridge, only a device for the owner
// to remove in Amazon's device list.
func Deregister(ctx context.Context, creds Credentials, opts ...Option) error {
	mp, ok := MarketplaceFor(creds.Marketplace)
	if !ok {
		return fmt.Errorf("unknown marketplace %q", creds.Marketplace)
	}
	if creds.AccessToken == "" || time.Until(time.Unix(creds.ExpiresAt, 0)) < accessTokenMargin {
		fresh, err := RefreshAccessToken(ctx, creds, opts...)
		if err != nil {
			return fmt.Errorf("deregister: %w", err)
		}
		creds = fresh
	}
	set := resolve(mp, authTimeout, opts)
	body, err := json.Marshal(map[string]bool{"deregister_all_existing_accounts": false})
	if err != nil {
		return err
	}
	if _, err := postAuth(ctx, set, "/auth/deregister", mp, "application/json", body, creds.AccessToken); err != nil {
		return fmt.Errorf("deregister: %w", err)
	}
	return nil
}
