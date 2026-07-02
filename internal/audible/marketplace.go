package audible

import "sort"

// Marketplace is an Audible storefront: the Amazon/Audible TLD and the marketplace
// id used in the OAuth authorize URL, keyed by a short locale code.
type Marketplace struct {
	Code          string
	TLD           string // amazon/audible domain suffix, e.g. "com", "co.uk"
	MarketPlaceID string
}

// marketplaces: us/uk/de are verified against the audible community libraries; the
// rest are the commonly-used ids from mkb79/audible and may need adjustment per
// storefront. The user picks their marketplace at login.
var marketplaces = map[string]Marketplace{
	"us": {"us", "com", "AF2M0KC94RCEA"},
	"uk": {"uk", "co.uk", "A2I9A3Q2GNFNGQ"},
	"de": {"de", "de", "AN7V1F1VY261K"},
	"fr": {"fr", "fr", "A2728XDNODOQ8T"},
	"ca": {"ca", "ca", "A2CQZ5RBY40XE"},
	"it": {"it", "it", "A2N7FU2W2BU2ZC"},
	"au": {"au", "com.au", "AN7EY7DTAW63G"},
	"in": {"in", "in", "AJO3FBRUE6J4S"},
	"jp": {"jp", "co.jp", "A1QAP3MOU4173J"},
	"es": {"es", "es", "ERVR1ADYTLTL2"},
	"br": {"br", "com.br", "A10J1VAYUDTYRN"},
}

// MarketplaceFor returns the marketplace for a locale code (e.g. "us").
func MarketplaceFor(code string) (Marketplace, bool) {
	m, ok := marketplaces[code]
	return m, ok
}

// MarketplaceCodes lists the supported locale codes, sorted.
func MarketplaceCodes() []string {
	codes := make([]string, 0, len(marketplaces))
	for c := range marketplaces {
		codes = append(codes, c)
	}
	sort.Strings(codes)
	return codes
}

// AmazonDomain is the login domain (www.amazon.<tld>).
func (m Marketplace) AmazonDomain() string { return "www.amazon." + m.TLD }

// APIDomain is the Audible API domain (api.audible.<tld>) - used for the library,
// license, and content endpoints.
func (m Marketplace) APIDomain() string { return "api.audible." + m.TLD }

// AmazonAPIDomain is the Amazon identity API domain (api.amazon.<tld>) - device
// registration goes here, NOT to the Audible API domain.
func (m Marketplace) AmazonAPIDomain() string { return "api.amazon." + m.TLD }

// CookieDomain is the cookie scope sent in the registration body (.amazon.<tld>).
func (m Marketplace) CookieDomain() string { return ".amazon." + m.TLD }
