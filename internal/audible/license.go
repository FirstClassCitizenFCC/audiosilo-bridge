package audible

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
)

type licenseResp struct {
	ContentLicense struct {
		LicenseResponse string `json:"license_response"` // base64 encrypted voucher
		ContentMetadata struct {
			ContentURL struct {
				OfflineURL string `json:"offline_url"` // presigned download URL
			} `json:"content_url"`
		} `json:"content_metadata"`
	} `json:"content_license"`
}

// License requests a download license for an owned book and unwraps the voucher,
// returning the (presigned) download URL plus the per-book ffmpeg key/iv.
func (c *Client) License(ctx context.Context, asin string) (downloadURL, key, iv string, err error) {
	body, _ := json.Marshal(map[string]any{
		"supported_drm_types": []string{"Mpeg", "Adrm"},
		"quality":             "High",
		"consumption_type":    "Download",
		"response_groups":     "last_position_heard,pdf_url,content_reference,chapter_info",
	})
	var lr licenseResp
	if err := c.doSigned(ctx, http.MethodPost, "/1.0/content/"+url.PathEscape(asin)+"/licenserequest", body, &lr); err != nil {
		return "", "", "", err
	}
	if lr.ContentLicense.LicenseResponse == "" {
		return "", "", "", fmt.Errorf("no license voucher returned for %s", asin)
	}
	key, iv, err = DecryptVoucher(lr.ContentLicense.LicenseResponse,
		c.creds.DeviceType, c.creds.DeviceSerial, c.creds.CustomerID, asin)
	if err != nil {
		return "", "", "", err
	}
	return lr.ContentLicense.ContentMetadata.ContentURL.OfflineURL, key, iv, nil
}
