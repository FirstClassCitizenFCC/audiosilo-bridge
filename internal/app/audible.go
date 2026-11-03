package app

import (
	"context"
	"errors"
	"log/slog"

	"github.com/kodestar/audiosilo-bridge/internal/api"
	"github.com/kodestar/audiosilo-bridge/internal/audible"
	"github.com/kodestar/audiosilo-bridge/internal/audibleacct"
	"github.com/kodestar/audiosilo-bridge/internal/audiblejob"
	"github.com/kodestar/audiosilo-bridge/internal/link"
	"github.com/kodestar/audiosilo-bridge/internal/secrets"
	"github.com/kodestar/audiosilo-bridge/internal/setup"
	"github.com/kodestar/audiosilo-bridge/internal/store"
)

// The Accounts tab's Audible section (Phase 4a): the accounts (audibleacct)
// and the audible.poll job (audiblejob). The server's Audible routes are not
// behind the console's module toggle (the podcasts precedent): signing in and
// mapping work whether or not the module is on, and the server queues polls
// only while it is.

// newAudible builds the Audible accounts over the vault and the poll handler.
func newAudible(vault secrets.Store, db *store.DB, log *slog.Logger, onChange func(), server func() *link.Client,
	serverID func() string) (*audibleacct.Manager, *audiblejob.Handler) {
	m := audibleacct.New(audibleacct.Options{Vault: vault, DB: db, Log: log, OnChange: onChange})
	h := audiblejob.New(audiblejob.Options{Accounts: m, ServerID: serverID, Log: log, Server: func() audiblejob.Server {
		if cl := server(); cl != nil {
			return cl
		}
		return nil
	}})
	return m, h
}

// audibleServer is the link for the accounts (nil while unpaired or refused).
func (b *Bridge) audibleServer() audibleacct.Server {
	if cl := b.conn.Client(); cl != nil {
		return cl
	}
	return nil
}

// Audible is the Audible section's state: the accounts, and the choices the
// forms offer (the server's books libraries and the people an account may be
// mapped to, read live).
func (b *Bridge) Audible(ctx context.Context) api.AudibleAccounts {
	out := api.AudibleAccounts{Accounts: []api.AudibleAccount{}, Marketplaces: audible.MarketplaceCodes(),
		Policies: audibleacct.Policies, Libraries: []api.AudibleLibrary{}, Users: []api.AudibleUser{}}
	names := map[int64]string{}
	for _, e := range b.libs.Entries(b.mount) {
		names[e.Library.ID] = e.Library.Name
		if e.Library.LibraryKind() == "books" {
			out.Libraries = append(out.Libraries, api.AudibleLibrary{ID: e.Library.ID, Name: e.Library.Name})
		}
	}
	serverID := b.conn.Pairing().ServerID
	accts, err := b.audible.Accounts(ctx)
	if err != nil {
		b.log.Warn("audible: listing the accounts failed", "err", err)
	}
	for _, a := range accts {
		out.Accounts = append(out.Accounts, api.AudibleAccount{ID: a.ID, Label: a.Label, Marketplace: a.Marketplace,
			UserID: a.UserID, UserName: a.UserName, LibraryID: a.LibraryID, LibraryName: names[a.LibraryID],
			Policy: a.Policy, Sync: a.Sync, CreatedAt: a.CreatedAt, LastOKAt: a.LastOKAt, LastPollAt: a.LastPollAt,
			LastTitles: a.LastTitles, LastMatched: a.LastMatched, Refused: a.RefusedAt != "", RefusedAt: a.RefusedAt,
			LastError: a.LastError, OtherServer: a.OnOtherServer(serverID)})
	}
	cl := b.conn.Client()
	out.Paired = cl != nil
	if cl == nil {
		out.UsersError = "pair the Bridge with a server first"
		return out
	}
	users, err := cl.Users(ctx)
	if err != nil {
		out.UsersError = "the server's people couldn't be read: " + err.Error()
		return out
	}
	for _, u := range users {
		out.Users = append(out.Users, api.AudibleUser{ID: u.ID, Name: u.DisplayName, Admin: u.Admin})
	}
	return out
}

// StartAudibleSignIn begins a sign-in.
func (b *Bridge) StartAudibleSignIn(_ context.Context, marketplace string) (api.AudibleSignIn, error) {
	s, err := b.audible.Start(marketplace)
	if err != nil {
		return api.AudibleSignIn{}, audibleError(err)
	}
	return api.AudibleSignIn{Login: s.Login, URL: s.URL, Marketplace: s.Marketplace, ExpiresAt: s.ExpiresAt}, nil
}

// FinishAudibleSignIn ends a sign-in with the pasted page and the mapping.
func (b *Bridge) FinishAudibleSignIn(ctx context.Context, f api.AudibleFinish) error {
	mp, err := b.audibleMapping(ctx, f.AudibleMapping)
	if err != nil {
		return err
	}
	_, err = b.audible.Finish(ctx, b.audibleServer(), b.conn.Pairing().ServerID, audibleacct.Finish{Login: f.Login, RedirectURL: f.RedirectURL, Mapping: mp})
	return audibleError(err)
}

// MapAudibleAccount maps an account again.
func (b *Bridge) MapAudibleAccount(ctx context.Context, id string, m api.AudibleMapping) error {
	mp, err := b.audibleMapping(ctx, m)
	if err != nil {
		return err
	}
	_, err = b.audible.Map(ctx, b.audibleServer(), b.conn.Pairing().ServerID, id, mp)
	return audibleError(err)
}

// RemoveAudibleAccount forgets an account, then removes it on the server and
// deregisters its device.
func (b *Bridge) RemoveAudibleAccount(ctx context.Context, id string) (string, error) {
	warning, err := b.audible.Remove(ctx, b.audibleServer(), id)
	return warning, audibleError(err)
}

// audibleMapping is the page's mapping with the user's name, checked against
// what the Bridge knows: a books library the server listed (the server checks
// again, and checks the user).
func (b *Bridge) audibleMapping(ctx context.Context, m api.AudibleMapping) (audibleacct.Mapping, error) {
	out := audibleacct.Mapping{Label: m.Label, UserID: m.UserID, LibraryID: m.LibraryID, Policy: m.Policy, Sync: m.Sync}
	if m.LibraryID > 0 {
		lib, ok := b.libs.Library(m.LibraryID)
		if !ok {
			return out, api.Errorf(api.ErrInvalid, "the server lists no library %d", m.LibraryID)
		}
		if lib.LibraryKind() != "books" {
			return out, api.Errorf(api.ErrInvalid, "%s isn't a books library", lib.Name)
		}
	}
	if cl := b.conn.Client(); cl != nil && m.UserID > 0 {
		if users, err := cl.Users(ctx); err == nil {
			for _, u := range users {
				if u.ID == m.UserID {
					out.UserName = u.DisplayName
				}
			}
			if out.UserName == "" {
				return out, api.Errorf(api.ErrInvalid, "the server has no person %d an account may be mapped to", m.UserID)
			}
		}
	}
	return out, nil
}

// audibleError maps an account error to the page's kinds; its message is the
// sentence the page shows (never a credential: audibleacct's errors carry
// Amazon's or the server's answer only).
func audibleError(err error) error {
	var apiErr *link.APIError
	switch {
	case err == nil:
		return nil
	case errors.Is(err, audibleacct.ErrInvalid):
		return api.Errorf(api.ErrInvalid, "%v", err)
	case errors.Is(err, audibleacct.ErrLoginExpired), errors.Is(err, audibleacct.ErrNotFound):
		return api.Errorf(api.ErrNotFound, "%v", err)
	case errors.Is(err, audibleacct.ErrNotPaired):
		return api.Errorf(api.ErrConflict, "%v", err)
	case errors.Is(err, audibleacct.ErrRegister), errors.Is(err, link.ErrUnauthorized):
		return api.Errorf(api.ErrRefused, "%v", err)
	case errors.As(err, &apiErr):
		return api.Errorf(api.ErrRefused, "the server refused the account: %s", apiErr.Message)
	}
	return err
}

// audibleSetup is the checklist's Audible input.
func (b *Bridge) audibleSetup(ctx context.Context) setup.Audible {
	accts, err := b.audible.Accounts(ctx)
	if err != nil {
		return setup.Audible{}
	}
	out := setup.Audible{Accounts: len(accts)}
	for _, a := range accts {
		if a.RefusedAt != "" {
			out.Refused++
		}
	}
	return out
}
