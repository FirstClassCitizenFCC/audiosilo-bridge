package audible

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"strings"
)

type licenseResp struct {
	ContentLicense struct {
		LicenseResponse string `json:"license_response"` // base64 encrypted voucher
		StatusCode      string `json:"status_code"`      // "Granted" / "Denied"
		DrmType         string `json:"drm_type"`         // "Adrm" (AAXC), "Mpeg" (a DRM-free MP3)
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
			ContentReference struct {
				// ContentSizeInBytes is the file's size, when Audible sends it.
				ContentSizeInBytes int64 `json:"content_size_in_bytes"`
				// ACR is the content reference a place written back needs
				// (ContentACR).
				ACR string `json:"acr"`
			} `json:"content_reference"`
		} `json:"content_metadata"`
	} `json:"content_license"`
}

// License is one book's download license: the (presigned) download URL, the
// file's size when Audible gave it (0 unknown) and the per-book key and iv
// ffmpeg decrypts it with. THE KEY AND IV ARE SECRETS: they are held in memory
// and handed only to ffmpeg's argv (Decrypt), never logged, stored, reported
// or put in an error. The URL is a bearer link too (its signature): it goes
// only to Download. Printing a License, logging it with slog or marshalling it
// to JSON shows none of them.
type License struct {
	URL  string
	Key  string
	IV   string
	Size int64
}

// String hides the license's secrets (fmt's %v and %s).
func (License) String() string { return "audible.License{redacted}" }

// GoString hides them from %#v too.
func (License) GoString() string { return "audible.License{redacted}" }

// LogValue hides them from slog.
func (License) LogValue() slog.Value { return slog.StringValue("[redacted]") }

// MarshalJSON refuses: a license never goes into JSON (a result, progress, the
// store).
func (License) MarshalJSON() ([]byte, error) {
	return nil, errors.New("audible: a license is never marshalled")
}

// Why Audible denied a license (License wraps one of these).
var (
	// ErrNotReleased: the title isn't out yet (a preorder).
	ErrNotReleased = errors.New("the title isn't released yet")
	// ErrNoRights: the account may not download the title (not owned, no
	// download rights, a periodical or show).
	ErrNoRights = errors.New("the account has no download rights for the title")
	// ErrThrottled: Audible denied the license to this device
	// (CustomerThrottled), as it does every license to a device registered
	// with the older profile (see ProfileVersion): signing in again registers
	// a device it licenses. Not the title's: the account's device.
	ErrThrottled = errors.New("audible refused this device a license (customer throttled): sign in again")
	// ErrDenied: Audible denied it for another reason.
	ErrDenied = errors.New("audible refused a download license")
	// ErrDRMFree: Audible granted the title only as a DRM-free MP3 (drm_type
	// Mpeg, no voucher), which the import doesn't back up yet. Not a denial.
	ErrDRMFree = errors.New("audible offers the title only as a DRM-free MP3")
)

// denialKind sorts a denied license into ErrThrottled, ErrNotReleased,
// ErrNoRights or ErrDenied by Audible's reasons (their validation type,
// rejection reason and message). Audible doesn't document them. A throttled
// device is checked first: its denial also carries an Ownership validation
// and two RequesterEligibility reasons (the owner's real run, Libation's
// classifier), which would otherwise read as no rights. Only the customer's
// throttling is: another "Throttled" (a rate limit, say) is ErrDenied, as
// signing in again wouldn't help it.
func denialKind(lr licenseResp) error {
	var words []string
	for _, r := range lr.ContentLicense.DenialReasons {
		words = append(words, strings.ToLower(r.ValidationType+" "+r.RejectionReason+" "+r.Message))
	}
	all := strings.Join(words, " ")
	switch {
	case strings.Contains(all, "customerthrottled") || strings.Contains(all, "being throttled"):
		return ErrThrottled
	case strings.Contains(all, "releas") || strings.Contains(all, "preorder") || strings.Contains(all, "pre-order"):
		return ErrNotReleased
	case strings.Contains(all, "ownership") || strings.Contains(all, "notowned") || strings.Contains(all, "not owned") ||
		strings.Contains(all, "rights") || strings.Contains(all, "eligib"):
		return ErrNoRights
	}
	return ErrDenied
}

// denialDetail summarizes why a license was denied, for the import error message
// ("" when Audible sent no usable detail), its account identifiers redacted
// (Audible's reasons quote the customer id), known ones (the account's own
// customer id and serial) by value too.
func denialDetail(lr licenseResp, known ...string) string {
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
	return RedactKnown(strings.Join(parts, "; "), known...)
}

// License requests a download license for an owned book and unwraps the
// voucher. A denial wraps ErrThrottled, ErrNotReleased, ErrNoRights or ErrDenied with
// Audible's reasons; a 401/403 is the account's refusal (IsRefused).
func (c *Client) License(ctx context.Context, asin string) (License, error) {
	body, _ := json.Marshal(map[string]any{
		"supported_drm_types": []string{"Mpeg", "Adrm"},
		"quality":             "High",
		"consumption_type":    "Download",
		"response_groups":     "last_position_heard,pdf_url,content_reference,chapter_info",
	})
	var lr licenseResp
	if err := c.doSigned(ctx, http.MethodPost, "/1.0/content/"+url.PathEscape(asin)+"/licenserequest", body, &lr); err != nil {
		return License{}, err
	}
	if strings.EqualFold(lr.ContentLicense.DrmType, "Mpeg") && !strings.EqualFold(lr.ContentLicense.StatusCode, "Denied") {
		return License{}, fmt.Errorf("%w: %s", ErrDRMFree, asin)
	}
	if lr.ContentLicense.LicenseResponse == "" {
		kind := denialKind(lr)
		if detail := denialDetail(lr, c.creds.CustomerID, c.creds.DeviceSerial); detail != "" {
			return License{}, fmt.Errorf("%w: %s (%s)", kind, asin, detail)
		}
		return License{}, fmt.Errorf("%w: no license voucher returned for %s", kind, asin)
	}
	key, iv, err := DecryptVoucher(lr.ContentLicense.LicenseResponse,
		c.creds.DeviceType, c.creds.DeviceSerial, c.creds.CustomerID, asin)
	if err != nil {
		return License{}, err
	}
	meta := lr.ContentLicense.ContentMetadata
	if meta.ContentURL.OfflineURL == "" {
		return License{}, fmt.Errorf("the license for %s carries no download URL", asin)
	}
	return License{URL: meta.ContentURL.OfflineURL, Key: key, IV: iv, Size: max(meta.ContentReference.ContentSizeInBytes, 0)}, nil
}

// ContentACR fetches the Audible Content Reference (ACR) for a book - required to
// write a last-heard position back to Audible (PUT /1.0/lastpositions/{asin}). It
// uses the same license request as a download (the only documented source of the
// ACR) but only reads content_reference.acr. An answer with no ACR is a denied
// license, wrapping its kind as License does (IsDenial): Audible answers it 200,
// so it carries no HTTP status.
func (c *Client) ContentACR(ctx context.Context, asin string) (string, error) {
	body, _ := json.Marshal(map[string]any{
		"supported_drm_types": []string{"Mpeg", "Adrm"},
		"quality":             "High",
		"consumption_type":    "Download",
		"response_groups":     "content_reference",
	})
	var r licenseResp
	if err := c.doSigned(ctx, http.MethodPost, "/1.0/content/"+url.PathEscape(asin)+"/licenserequest", body, &r); err != nil {
		return "", err
	}
	if acr := r.ContentLicense.ContentMetadata.ContentReference.ACR; acr != "" {
		return acr, nil
	}
	return "", fmt.Errorf("%w: no content reference (acr) returned for %s", denialKind(r), asin)
}

// IsDenial reports whether err is Audible denying a license (ErrThrottled,
// ErrNotReleased, ErrNoRights or ErrDenied): an answer about the title or the
// device, not a failure to answer.
func IsDenial(err error) bool {
	return errors.Is(err, ErrThrottled) || errors.Is(err, ErrNotReleased) || errors.Is(err, ErrNoRights) ||
		errors.Is(err, ErrDenied)
}
