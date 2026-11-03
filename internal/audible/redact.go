package audible

import (
	"regexp"
	"slices"
	"strings"
)

// Redacted stands in for an account identifier RedactAccountIDs removed.
const Redacted = "[redacted]"

// redactedBare is Redacted inside brackets the text already has
// ("Customer id [redacted]").
const redactedBare = "redacted"

// Amazon's error text quotes the account it refused: a license denial reads
// "Customer id [A1B2C3D4E5F6G7] being throttled; ... does not has access to
// asin[B00H3R0B0W]". Every error the package builds from an Amazon answer
// (an HTTP error body, a license's denial reasons) passes through
// RedactAccountIDs before it exists as text, so no log line, job error, result,
// progress or anything sent to the server carries one. The ASIN and the human
// reason stay.
var (
	// A labelled customer id, directed id or device serial: the label stays,
	// its value goes ("Customer id [X]", "customer_id": "X", serial=X).
	labelledID = regexp.MustCompile(`(?i)\b((?:customer|directed|user|account)[ _-]?id|device[ _-]?serial(?:[ _-]?number)?|serial[ _-]?number)(\s*"?\s*[:=\[]\s*"?\s*)([^\[\]\s",;&}]+)`)
	// A token or secret given as key=value or "key": "value".
	labelledSecret = regexp.MustCompile(`(?i)\b((?:access|refresh|adp|session|id|auth)?[ _-]?token|password|cookie|authorization|private[ _-]?key)("?\s*[:=]\s*"?)([^"\s,;&}]+)`)
	// Amazon's directed ids and account ids: amzn1.account.X, amzn1.actor.X,
	// amzn1.application-oa2-client.X.
	amazonID = regexp.MustCompile(`amzn1\.[A-Za-z0-9._-]+`)
	// Amazon's own tokens (access Atna|, refresh Atzr|, Atnr|, Atza|).
	amazonToken = regexp.MustCompile(`\bAt(?:na|nr|zr|za)\|[A-Za-z0-9._~+/=|-]+`)
	// A bearer or basic credential in a quoted header.
	bearer = regexp.MustCompile(`(?i)\b(bearer|basic)\s+[A-Za-z0-9._~+/=-]+`)
	email  = regexp.MustCompile(`[A-Za-z0-9._%+-]+@[A-Za-z0-9-]+(?:\.[A-Za-z0-9-]+)*\.[A-Za-z]{2,}`)
	// An unlabelled customer id: A then 10 or more capitals and digits, with a
	// digit (an all-capitals word like ACCESSDENIED is a reason, not an id).
	// ASINs start B0 or are 10 digits, so none matches.
	customerID = regexp.MustCompile(`\bA[0-9A-Z]{10,}\b`)
	// A device serial (32 hex, as the older profile's were) or a longer hex run.
	hexRun = regexp.MustCompile(`(?i)\b[0-9a-f]{32,}\b`)
	// A device serial as NewDeviceSerial makes them: 40 letters and digits.
	serial40 = regexp.MustCompile(`\b[A-Za-z0-9]{40}\b`)
)

// RedactAccountIDs removes Amazon account identifiers from s: customer ids
// (labelled or a bare A... id), amzn1.* directed and account ids, device
// serials (labelled, 32 or more hex, or 40 letters and digits), email addresses and tokens (bearer, Amazon's Atna|/Atzr|, key=value).
// ASINs (B0..., 10 digits) and the human-readable reason are kept.
func RedactAccountIDs(s string) string {
	if s == "" {
		return s
	}
	s = amazonToken.ReplaceAllLiteralString(s, Redacted)
	s = bearer.ReplaceAllString(s, "${1} "+Redacted)
	s = redactValues(labelledID, s)
	s = redactValues(labelledSecret, s)
	s = amazonID.ReplaceAllLiteralString(s, Redacted)
	s = email.ReplaceAllLiteralString(s, Redacted)
	s = customerID.ReplaceAllStringFunc(s, func(m string) string {
		if strings.ContainsAny(m, "0123456789") {
			return Redacted
		}
		return m
	})
	s = hexRun.ReplaceAllLiteralString(s, Redacted)
	return serial40.ReplaceAllLiteralString(s, Redacted)
}

// minKnown is the shortest known value RedactKnown removes: anything shorter
// is no identifier (and would cut words apart).
const minKnown = 8

// RedactKnown is RedactAccountIDs that first removes each of known (an
// account's own customer id and device serial, values the caller holds)
// wherever it appears, whatever its shape.
func RedactKnown(s string, known ...string) string {
	for _, k := range known {
		if len(k) >= minKnown {
			s = strings.ReplaceAll(s, k, Redacted)
		}
	}
	return RedactAccountIDs(s)
}

// Redact returns err with its text redacted of the account's own identifiers
// (its customer id and device serial, by value) and every shape
// RedactAccountIDs knows; errors.Is and errors.As still see err.
func (c Credentials) Redact(err error) error {
	return redactedErr(err, c.CustomerID, c.DeviceSerial)
}

// redactValues replaces the value (the third group) of each of re's matches in
// s, keeping the label and separator. A value already redacted stays as it is,
// so redacting twice changes nothing.
func redactValues(re *regexp.Regexp, s string) string {
	return re.ReplaceAllStringFunc(s, func(m string) string {
		g := re.FindStringSubmatch(m)
		if g[3] == Redacted || g[3] == redactedBare {
			return m
		}
		if strings.HasSuffix(strings.TrimSpace(g[2]), "[") {
			return g[1] + g[2] + redactedBare // "Customer id [redacted]"
		}
		return g[1] + g[2] + Redacted
	})
}

// RedactedError is err with its text passed through RedactAccountIDs; errors.Is
// and errors.As still see err. Nil stays nil.
func RedactedError(err error) error { return redactedErr(err) }

// redactedErr is err redacted (RedactKnown with known); one already redacted
// gains known rather than a second wrapper.
func redactedErr(err error, known ...string) error {
	if err == nil {
		return nil
	}
	if r, ok := err.(*redactedError); ok { //nolint:errorlint // only err itself: a wrapper around one may add text
		if len(known) == 0 {
			return r
		}
		return &redactedError{err: r.err, known: append(slices.Clip(r.known), known...)}
	}
	return &redactedError{err: err, known: known}
}

type redactedError struct {
	err   error
	known []string
}

func (e *redactedError) Error() string { return RedactKnown(e.err.Error(), e.known...) }
func (e *redactedError) Unwrap() error { return e.err }
