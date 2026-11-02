package audible

import (
	"crypto/rsa"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"fmt"
)

// Credentials is the durable result of registering a device with Audible. It is
// full Amazon account access (the device private key + adp_token sign unrestricted
// API calls), so it is stored ONLY in the OS keychain, never the registry file.
type Credentials struct {
	AccessToken      string `json:"access_token"`  // ~60 min; refreshed via RefreshToken
	RefreshToken     string `json:"refresh_token"` // durable
	ADPToken         string `json:"adp_token"`     // durable; x-adp-token header
	DevicePrivateKey string `json:"device_private_key"`
	DeviceType       string `json:"device_type"`
	DeviceSerial     string `json:"device_serial"`
	CustomerID       string `json:"customer_id"`
	Marketplace      string `json:"marketplace"` // locale code (see Marketplace)
	ExpiresAt        int64  `json:"expires_at"`  // unix seconds for AccessToken
	ActivationBytes  string `json:"activation_bytes,omitempty"`
}

// Marshal/Unmarshal round-trip the credentials for keychain storage.
func (c Credentials) Marshal() (string, error) {
	b, err := json.Marshal(c)
	return string(b), err
}

// ParseCredentials parses keychain-stored credentials JSON.
func ParseCredentials(s string) (Credentials, error) {
	var c Credentials
	err := json.Unmarshal([]byte(s), &c)
	return c, err
}

// PrivateKey parses the device RSA key (PKCS#1 on iOS device types, PKCS#8 on
// Android - try both).
func (c Credentials) PrivateKey() (*rsa.PrivateKey, error) {
	block, _ := pem.Decode([]byte(c.DevicePrivateKey))
	if block == nil {
		return nil, fmt.Errorf("device private key is not PEM")
	}
	if k, err := x509.ParsePKCS1PrivateKey(block.Bytes); err == nil {
		return k, nil
	}
	any, err := x509.ParsePKCS8PrivateKey(block.Bytes)
	if err != nil {
		return nil, fmt.Errorf("parse device private key: %w", err)
	}
	k, ok := any.(*rsa.PrivateKey)
	if !ok {
		return nil, fmt.Errorf("device private key is not RSA")
	}
	return k, nil
}
