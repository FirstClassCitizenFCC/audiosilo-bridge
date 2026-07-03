package audible

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strings"
)

type licenseResp struct {
	ContentLicense struct {
		LicenseResponse string `json:"license_response"` // base64 encrypted voucher
		StatusCode      string `json:"status_code"`      // "Granted" / "Denied"
		// DenialReasons explain a Denied license (mixed-case keys are Audible's).
		DenialReasons []struct {
			Message         string `json:"message"`
			RejectionReason string `json:"rejectionReason"`
			ValidationType  string `json:"validationType"`
		} `json:"license_denial_reasons"`
		ContentMetadata struct {
			ContentURL struct {
				OfflineURL string `json:"offline_url"` // presigned download URL
			} `json:"content_url"`
		} `json:"content_metadata"`
	} `json:"content_license"`
}

// denialDetail summarizes why a license was denied, for the import error message
// ("" when Audible sent no usable detail).
func denialDetail(lr licenseResp) string {
	var parts []string
	for _, r := range lr.ContentLicense.DenialReasons {
		s := strings.TrimSpace(firstNonEmptyStr(r.Message, r.ValidationType+" "+r.RejectionReason))
		if s != "" {
			parts = append(parts, s)
		}
	}
	if len(parts) == 0 && lr.ContentLicense.StatusCode != "" && lr.ContentLicense.StatusCode != "Granted" {
		return "status " + lr.ContentLicense.StatusCode
	}
	return strings.Join(parts, "; ")
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
		if detail := denialDetail(lr); detail != "" {
			return "", "", "", fmt.Errorf("audible refused a download license for %s (%s)", asin, detail)
		}
		return "", "", "", fmt.Errorf("no license voucher returned for %s", asin)
	}
	key, iv, err = DecryptVoucher(lr.ContentLicense.LicenseResponse,
		c.creds.DeviceType, c.creds.DeviceSerial, c.creds.CustomerID, asin)
	if err != nil {
		return "", "", "", err
	}
	return lr.ContentLicense.ContentMetadata.ContentURL.OfflineURL, key, iv, nil
}

type contentRefResp struct {
	ContentLicense struct {
		ContentMetadata struct {
			ContentReference struct {
				ACR string `json:"acr"`
			} `json:"content_reference"`
		} `json:"content_metadata"`
	} `json:"content_license"`
}

// ContentACR fetches the Audible Content Reference (ACR) for a book - required to
// write a last-heard position back to Audible (PUT /1.0/lastpositions/{asin}). It
// uses the same license request as a download (the only documented source of the
// ACR) but only reads content_reference.acr.
func (c *Client) ContentACR(ctx context.Context, asin string) (string, error) {
	body, _ := json.Marshal(map[string]any{
		"supported_drm_types": []string{"Mpeg", "Adrm"},
		"quality":             "High",
		"consumption_type":    "Download",
		"response_groups":     "content_reference",
	})
	var r contentRefResp
	if err := c.doSigned(ctx, http.MethodPost, "/1.0/content/"+url.PathEscape(asin)+"/licenserequest", body, &r); err != nil {
		return "", err
	}
	if r.ContentLicense.ContentMetadata.ContentReference.ACR == "" {
		return "", fmt.Errorf("no content reference (acr) returned for %s", asin)
	}
	return r.ContentLicense.ContentMetadata.ContentReference.ACR, nil
}
