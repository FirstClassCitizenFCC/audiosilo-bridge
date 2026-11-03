package audible

import (
	"regexp"
	"strings"
)

// Redacted stands in for an account identifier RedactAccountIDs removed.
const Redacted = "[redacted]"

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
	// A device serial (32 hex, as NewDeviceSerial makes) or a longer hex run.
	hexRun = regexp.MustCompile(`(?i)\b[0-9a-f]{32,}\b`)
)

// RedactAccountIDs removes Amazon account identifiers from s: customer ids
// (labelled or a bare A... id), amzn1.* directed and account ids, device
// serials, email addresses and tokens (bearer, Amazon's Atna|/Atzr|, key=value).
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
	return hexRun.ReplaceAllLiteralString(s, Redacted)
}

// redactValues replaces the value (the third group) of each of re's matches in
// s, keeping the label and separator. A value already redacted stays as it is,
// so redacting twice changes nothing.
func redactValues(re *regexp.Regexp, s string) string {
	return re.ReplaceAllStringFunc(s, func(m string) string {
		g := re.FindStringSubmatch(m)
		if g[3] == Redacted || g[3] == strings.Trim(Redacted, "[]") {
			return m
		}
		if strings.HasSuffix(strings.TrimSpace(g[2]), "[") {
			return g[1] + g[2] + strings.Trim(Redacted, "[]") // "Customer id [redacted]"
		}
		return g[1] + g[2] + Redacted
	})
}

// RedactedError is err with its text passed through RedactAccountIDs; errors.Is
// and errors.As still see err. Nil stays nil.
func RedactedError(err error) error {
	if err == nil {
		return nil
	}
	if r, ok := err.(*redactedError); ok { //nolint:errorlint // only err itself: a wrapper around one may add text
		return r
	}
	return &redactedError{err: err}
}

type redactedError struct{ err error }

func (e *redactedError) Error() string { return RedactAccountIDs(e.err.Error()) }
func (e *redactedError) Unwrap() error { return e.err }
