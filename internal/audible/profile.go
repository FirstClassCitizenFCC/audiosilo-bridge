package audible

import "crypto/rand"

// The device profile: the Audible for iPhone app a device registers as, and
// the app version its token refreshes and deregistration name. Since about
// September 2026 Amazon denies a download license (Ownership /
// CustomerThrottled, beside two RequesterEligibility reasons) to a device
// registered with the older profile (app 3.56.2, iOS 15 or 17, software
// 35602678, a 32-character uppercase-hex serial) and grants one registered
// with this one (mkb79/Audible#909 and PR #910, audible 0.13.1; Libation's
// device-registration notes agree). One set: change the three together, and
// ProfileVersion changes with them, so the accounts registered before show as
// needing a sign-in again.
const (
	appVersion      = "4.60"
	osVersion       = "26.2"
	softwareVersion = "46000856"

	// ProfileVersion names the profile a registration used. Register records
	// it in the Credentials it returns; an account whose sign-in carries
	// another (or none: one made before the profile was recorded) must sign in
	// again to get licenses.
	ProfileVersion = appVersion + "/" + osVersion + "/" + softwareVersion
)

// serialLen and serialAlphabet are the official app's serial shape: 40 ASCII
// letters and digits.
const (
	serialLen      = 40
	serialAlphabet = "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789"
)

// NewDeviceSerial returns a random device serial: 40 ASCII letters and digits
// from crypto/rand, uniformly (a byte past the alphabet's last whole multiple
// is drawn again).
func NewDeviceSerial() (string, error) {
	const limit = 256 - 256%len(serialAlphabet)
	out := make([]byte, 0, serialLen)
	buf := make([]byte, 2*serialLen)
	for len(out) < serialLen {
		if _, err := rand.Read(buf); err != nil {
			return "", err
		}
		for _, b := range buf {
			if int(b) < limit && len(out) < serialLen {
				out = append(out, serialAlphabet[int(b)%len(serialAlphabet)])
			}
		}
	}
	return string(out), nil
}
