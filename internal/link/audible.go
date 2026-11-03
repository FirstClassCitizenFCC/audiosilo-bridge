package link

import (
	"context"
	"errors"
	"net/http"
	"net/url"
	"strconv"
)

// Phase 4a: Audible. The server's routes for the accounts this Bridge signs in
// to (its handlers_audible.go, mirrored BY HAND): who an account may be mapped
// to, an account's mapping, the titles a poll found, a library's books for the
// matching and an ASIN backfill. Every route takes the bridge token in the
// header only.

// User is one person an Audible account may be mapped to (GET /bridge/users:
// enabled adults and admins; never a kid, demo or disabled account, never an
// email).
type User struct {
	ID          int64  `json:"id"`
	Username    string `json:"username"`
	DisplayName string `json:"display_name"`
	Admin       bool   `json:"admin"`
}

// AudibleAccountInput is an account's mapping (PUT
// /bridge/audible/accounts/{account}): the server user it belongs to, the books
// library its titles go to, the policy (all | series | manual) and whether its
// places sync.
type AudibleAccountInput struct {
	Label       string `json:"label"`
	Marketplace string `json:"marketplace"`
	UserID      int64  `json:"user_id"`
	LibraryID   int64  `json:"library_id"`
	Policy      string `json:"policy"`
	Sync        bool   `json:"sync"`
}

// AudibleAccount is the server's record of an account (the part the Bridge
// reads back). LibraryID is nil while the account is paused (its library was
// deleted on the server); PollHours is how often the server queues its poll.
type AudibleAccount struct {
	ID          string `json:"id"`
	Label       string `json:"label"`
	Marketplace string `json:"marketplace"`
	UserID      int64  `json:"user_id"`
	User        struct {
		ID          int64  `json:"id"`
		DisplayName string `json:"display_name"`
	} `json:"user"`
	LibraryID *int64 `json:"library_id"`
	Paused    bool   `json:"paused"`
	Policy    string `json:"policy"`
	Sync      bool   `json:"sync"`
	PollHours int    `json:"poll_hours"`
}

// The Audible title states a poll reports.
const (
	AudibleAvailable   = "available"
	AudiblePreorder    = "preorder"
	AudibleUnavailable = "unavailable"
)

// The ways a title matched a book.
const (
	MatchASIN  = "asin"
	MatchFuzzy = "fuzzy"
)

// MaxAudibleTitles is the most titles one PUT .../titles call carries (the
// server's MaxAudibleTitles); MaxLibraryBooks the largest page of a library's
// books (its MaxBridgeBooks).
const (
	MaxAudibleTitles = 500
	MaxLibraryBooks  = 500
)

// AudibleMatch is the book a title matched: the library, the book's path and
// how (MatchASIN, MatchFuzzy).
type AudibleMatch struct {
	LibraryID int64  `json:"library_id"`
	Path      string `json:"path"`
	How       string `json:"how"`
}

// AudibleTitle is one title of an account's library as a poll reports it.
// ReleaseDate is YYYY-MM-DD or ""; Reason says why an unavailable title can't
// be downloaded; Match is nil when no book matched.
type AudibleTitle struct {
	ASIN        string        `json:"asin"`
	Title       string        `json:"title"`
	Authors     []string      `json:"authors"`
	Series      []SeriesRef   `json:"series"`
	ReleaseDate string        `json:"release_date"`
	RuntimeS    int64         `json:"runtime_s"`
	State       string        `json:"state"`
	Reason      string        `json:"reason"`
	Match       *AudibleMatch `json:"match"`
}

// AudibleTitles is one PUT .../titles call: a page of one poll's titles (at
// most MaxAudibleTitles). On the poll's Final call the server marks every
// title of the account the poll didn't report as gone.
type AudibleTitles struct {
	PollID string         `json:"poll_id"`
	Final  bool           `json:"final"`
	Titles []AudibleTitle `json:"titles"`
}

// Errors of the Audible routes.
var (
	// ErrASINSet is an enrichment's 409 asin_set: the book has another ASIN.
	ErrASINSet = errors.New("the book already has another ASIN")
	// ErrUnknownAccount is a 404 unknown_account: the server has no such
	// Audible account (it was removed there, or this is another server).
	ErrUnknownAccount = errors.New("the server has no such Audible account")
)

// audiblePath is an account's route.
func audiblePath(account string) string {
	return "/api/v1/bridge/audible/accounts/" + url.PathEscape(account)
}

// Users lists who an Audible account may be mapped to.
func (c *Client) Users(ctx context.Context) ([]User, error) {
	var out struct {
		Users []User `json:"users"`
	}
	if err := c.do(ctx, http.MethodGet, "/api/v1/bridge/users", nil, nil, &out); err != nil {
		return nil, err
	}
	if out.Users == nil {
		out.Users = []User{}
	}
	return out.Users, nil
}

// PutAudibleAccount records account's mapping on the server (a new account,
// or one mapped again) and returns the server's record of it.
func (c *Client) PutAudibleAccount(ctx context.Context, account string, in AudibleAccountInput) (*AudibleAccount, error) {
	var out struct {
		Account AudibleAccount `json:"account"`
	}
	if err := c.do(ctx, http.MethodPut, audiblePath(account), nil, in, &out); err != nil {
		return nil, err
	}
	return &out.Account, nil
}

// DeleteAudibleAccount removes account on the server (its titles go with it).
// The server answers 204 whether it knew the account or not.
func (c *Client) DeleteAudibleAccount(ctx context.Context, account string) error {
	return c.do(ctx, http.MethodDelete, audiblePath(account), nil, nil, nil)
}

// PutAudibleTitles stores one call of a poll's titles and returns how many the
// server stored. ErrUnknownAccount: the server has no such account.
func (c *Client) PutAudibleTitles(ctx context.Context, account string, t AudibleTitles) (int, error) {
	if t.Titles == nil {
		t.Titles = []AudibleTitle{}
	}
	var out struct {
		Stored int `json:"stored"`
	}
	err := c.do(ctx, http.MethodPut, audiblePath(account)+"/titles", nil, t, &out)
	if e := (*APIError)(nil); errors.As(err, &e) && e.Status == http.StatusNotFound && e.Code == "unknown_account" {
		return 0, ErrUnknownAccount
	}
	if err != nil {
		return 0, err
	}
	return out.Stored, nil
}

// LibraryBooks reads one page of a books library's indexed books, by path:
// after is the previous page's Next ("" for the first), limit at most
// MaxLibraryBooks (0 for the server's default). Next is "" on the last page.
func (c *Client) LibraryBooks(ctx context.Context, libraryID int64, after string, limit int) (*LibraryBooks, error) {
	q := url.Values{}
	if after != "" {
		q.Set("after", after)
	}
	if limit > 0 {
		q.Set("limit", strconv.Itoa(limit))
	}
	var out LibraryBooks
	if err := c.do(ctx, http.MethodGet, "/api/v1/bridge/libraries/"+strconv.FormatInt(libraryID, 10)+"/books", q, nil, &out); err != nil {
		return nil, err
	}
	if out.Books == nil {
		out.Books = []LibraryBook{}
	}
	return &out, nil
}

// Enrich backfills asin onto the book at path, which has none (an Audible
// title matched to it without doubt). ErrASINSet: it has another; ErrNoBook:
// no book is indexed there now.
func (c *Client) Enrich(ctx context.Context, libraryID int64, path, asin string) error {
	err := c.do(ctx, http.MethodPut, "/api/v1/bridge/libraries/"+strconv.FormatInt(libraryID, 10)+"/enrichment",
		url.Values{"path": {path}}, map[string]string{"asin": asin}, nil)
	var e *APIError
	switch {
	case errors.As(err, &e) && e.Status == http.StatusConflict && e.Code == "asin_set":
		return ErrASINSet
	case errors.As(err, &e) && e.Status == http.StatusNotFound:
		return ErrNoBook
	}
	return err
}
