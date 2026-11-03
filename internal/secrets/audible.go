package secrets

// AudiblePrefix starts the name an Audible account's sign-in is sealed under.
const AudiblePrefix = "audible_"

// AudibleName names an Audible account's sealed sign-in (Phase 4a): the
// tokens and device key of audible.Credentials, full access to the Amazon
// account. id is the account's own ([a-z0-9]{8,32}). Not in Names(): the
// Accounts tab's Audible section sets it, never an operator by hand.
func AudibleName(id string) string { return AudiblePrefix + id }
