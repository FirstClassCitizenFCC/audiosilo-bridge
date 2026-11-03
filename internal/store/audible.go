package store

import (
	"context"
	"database/sql"
	"errors"
	"time"
)

// AudibleAccount is what the Bridge keeps about one Audible account (migration
// 0009): its label, marketplace and mapping, and how its polls went. The
// sign-in itself is in the vault (audible_<id>), never here.
type AudibleAccount struct {
	ID          string `json:"id"`
	Label       string `json:"label"`
	Marketplace string `json:"marketplace"`
	// CustomerHash is the sha256 of the Amazon customer id (never the id).
	CustomerHash string `json:"-"`
	// ServerID is the server the mapping was last recorded on.
	ServerID    string `json:"server_id"`
	UserID      int64  `json:"user_id"`
	UserName    string `json:"user_name"`
	LibraryID   int64  `json:"library_id"`
	Policy      string `json:"policy"`
	Sync        bool   `json:"sync"`
	CreatedAt   string `json:"created_at"`
	LastOKAt    string `json:"last_ok_at"`
	LastPollAt  string `json:"last_poll_at"`
	LastTitles  int    `json:"last_titles"`
	LastMatched int    `json:"last_matched"`
	RefusedAt   string `json:"refused_at"`
	LastError   string `json:"last_error"`
	// DeviceProfile is the device profile the sign-in registered with
	// (audible.ProfileVersion then, migration 0010; "" before it was kept).
	DeviceProfile string `json:"device_profile"`
}

// OnOtherServer reports whether a is mapped on a server other than serverID
// (the pairing's; "" while unpaired, which is never "other").
func (a AudibleAccount) OnOtherServer(serverID string) bool {
	return serverID != "" && a.ServerID != serverID
}

const audibleColumns = `id, label, marketplace, customer_hash, server_id, user_id, user_name, library_id, policy, sync,
	created_at, last_ok_at, last_poll_at, last_titles, last_matched, refused_at, last_error, device_profile`

func scanAudibleAccount(row interface{ Scan(...any) error }) (AudibleAccount, error) {
	var a AudibleAccount
	err := row.Scan(&a.ID, &a.Label, &a.Marketplace, &a.CustomerHash, &a.ServerID, &a.UserID, &a.UserName, &a.LibraryID,
		&a.Policy, &a.Sync, &a.CreatedAt, &a.LastOKAt, &a.LastPollAt, &a.LastTitles, &a.LastMatched, &a.RefusedAt, &a.LastError, &a.DeviceProfile)
	return a, err
}

// AudibleAccounts lists the accounts, oldest first.
func (db *DB) AudibleAccounts(ctx context.Context) ([]AudibleAccount, error) {
	rows, err := db.sql.QueryContext(ctx, `SELECT `+audibleColumns+` FROM audible_accounts ORDER BY created_at, id`)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	out := []AudibleAccount{}
	for rows.Next() {
		a, err := scanAudibleAccount(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, a)
	}
	return out, rows.Err()
}

// GetAudibleAccount returns one account, or ErrNotFound.
func (db *DB) GetAudibleAccount(ctx context.Context, id string) (AudibleAccount, error) {
	a, err := scanAudibleAccount(db.sql.QueryRowContext(ctx, `SELECT `+audibleColumns+` FROM audible_accounts WHERE id = ?`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return AudibleAccount{}, ErrNotFound
	}
	return a, err
}

// AudibleAccountByCustomer returns the account signed in to the Amazon account
// whose customer id hashes to hash in marketplace, or ErrNotFound.
func (db *DB) AudibleAccountByCustomer(ctx context.Context, hash, marketplace string) (AudibleAccount, error) {
	if hash == "" {
		return AudibleAccount{}, ErrNotFound
	}
	// customer_hash <> '' lets SQLite use the partial unique index.
	a, err := scanAudibleAccount(db.sql.QueryRowContext(ctx, `SELECT `+audibleColumns+` FROM audible_accounts
		WHERE customer_hash = ? AND customer_hash <> '' AND marketplace = ?`, hash, marketplace))
	if errors.Is(err, sql.ErrNoRows) {
		return AudibleAccount{}, ErrNotFound
	}
	return a, err
}

// SignInAudibleAccount records a new sign-in of a (insert, or a known id's
// sign-in replaced): its label, marketplace, customer hash, mapping and device
// profile are a's, it works as of at, and any refusal or error is forgotten. A
// known account keeps its created_at and poll counts.
func (db *DB) SignInAudibleAccount(ctx context.Context, a AudibleAccount, at time.Time) error {
	ts := timestamp(at)
	_, err := db.sql.ExecContext(ctx, `INSERT INTO audible_accounts (id, label, marketplace, customer_hash, server_id, user_id,
			user_name, library_id, policy, sync, created_at, last_ok_at, device_profile)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(id) DO UPDATE SET label = excluded.label, marketplace = excluded.marketplace,
			customer_hash = excluded.customer_hash, server_id = excluded.server_id, user_id = excluded.user_id,
			user_name = excluded.user_name, library_id = excluded.library_id, policy = excluded.policy,
			sync = excluded.sync, last_ok_at = excluded.last_ok_at, device_profile = excluded.device_profile,
			refused_at = '', last_error = ''`,
		a.ID, a.Label, a.Marketplace, a.CustomerHash, a.ServerID, a.UserID, a.UserName, a.LibraryID, a.Policy, a.Sync, ts, ts,
		a.DeviceProfile)
	return err
}

// MapAudibleAccount records a's mapping (label, server, user, library, policy,
// sync) on its row. ErrNotFound: no such account.
func (db *DB) MapAudibleAccount(ctx context.Context, a AudibleAccount) error {
	res, err := db.sql.ExecContext(ctx, `UPDATE audible_accounts SET label = ?, server_id = ?, user_id = ?, user_name = ?,
		library_id = ?, policy = ?, sync = ? WHERE id = ?`, a.Label, a.ServerID, a.UserID, a.UserName, a.LibraryID, a.Policy, a.Sync, a.ID)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

// AudiblePolled records a complete poll of id at at: Audible answered (a
// refusal ends), titles and matched are its counts, and the last error clears.
// A refusal recorded with keep as its reason stays (a device Audible denies
// licenses still lists its library: only a new sign-in ends that one).
func (db *DB) AudiblePolled(ctx context.Context, id string, titles, matched int, at time.Time, keep string) error {
	ts := timestamp(at)
	_, err := db.sql.ExecContext(ctx, `UPDATE audible_accounts SET last_ok_at = ?, last_poll_at = ?, last_titles = ?,
		last_matched = ?,
		refused_at = CASE WHEN refused_at <> '' AND last_error = ? THEN refused_at ELSE '' END,
		last_error = CASE WHEN refused_at <> '' AND last_error = ? THEN last_error ELSE '' END
		WHERE id = ?`, ts, ts, titles, matched, keep, keep, id)
	return err
}

// RefuseAudibleAccount records that Audible refused id's sign-in at at, and why.
func (db *DB) RefuseAudibleAccount(ctx context.Context, id, why string, at time.Time) error {
	_, err := db.sql.ExecContext(ctx, `UPDATE audible_accounts SET refused_at = ?, last_error = ? WHERE id = ?`,
		timestamp(at), why, id)
	return err
}

// AudibleAccountError records why id's last poll failed (not a refusal). A
// refused account keeps its refusal's reason: that is what it needs fixed.
func (db *DB) AudibleAccountError(ctx context.Context, id, why string) error {
	_, err := db.sql.ExecContext(ctx, `UPDATE audible_accounts SET last_error = ? WHERE id = ? AND refused_at = ''`, why, id)
	return err
}

// DeleteAudibleAccount forgets id.
func (db *DB) DeleteAudibleAccount(ctx context.Context, id string) error {
	_, err := db.sql.ExecContext(ctx, `DELETE FROM audible_accounts WHERE id = ?`, id)
	return err
}
