package store

import (
	"context"
	"database/sql"
	"errors"
	"time"
)

// AudiblePush is one (account, ASIN) of the Audible position sync (migration
// 0011): the book the server's last audible.sync listed for it (Listed false:
// it wasn't listed the last time, so neither direction touches it), Audible's
// runtime, the cached content reference, the place last written to Audible
// (LastMs, -1 none) and when, the Audible place the last sync sent to the
// server (HeardMs, -1 none), and the place waiting to be pushed (WantMs, nil
// none) with the server row's updated_at (RowUpdated, the newest seen).
type AudiblePush struct {
	Account    string
	ASIN       string
	LibraryID  int64
	Path       string
	RuntimeS   int64
	Listed     bool
	ACR        string
	LastMs     int64
	At         string
	HeardMs    int64
	WantMs     *int64
	RowUpdated string
}

// AudibleSyncTitle is one title an audible.sync job lists: the ASIN, the book
// it matched and Audible's runtime (seconds, 0 unknown).
type AudibleSyncTitle struct {
	ASIN      string
	LibraryID int64
	Path      string
	RuntimeS  int64
}

const audiblePushColumns = `account, asin, library_id, path, runtime_s, listed, acr, last_ms, at, heard_ms, want_ms, row_updated`

func scanAudiblePush(row interface{ Scan(...any) error }) (AudiblePush, error) {
	var p AudiblePush
	var want sql.NullInt64
	err := row.Scan(&p.Account, &p.ASIN, &p.LibraryID, &p.Path, &p.RuntimeS, &p.Listed, &p.ACR, &p.LastMs, &p.At, &p.HeardMs,
		&want, &p.RowUpdated)
	if want.Valid {
		p.WantMs = &want.Int64
	}
	return p, err
}

// ListAudibleSync records the titles an audible.sync job of account lists:
// each one's book and runtime, listed; every other row of the account is
// unlisted (a title the console stopped syncing, or one that lost its match),
// and its waiting place forgotten. The pushes, places heard and cached
// content references stay. A title whose book changed forgets its waiting
// place too.
func (db *DB) ListAudibleSync(ctx context.Context, account string, titles []AudibleSyncTitle) error {
	tx, err := db.sql.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	if _, err := tx.ExecContext(ctx, `UPDATE audible_push SET listed = 0 WHERE account = ?`, account); err != nil {
		return err
	}
	upsert, err := tx.PrepareContext(ctx, `INSERT INTO audible_push (account, asin, library_id, path, runtime_s, listed)
		VALUES (?, ?, ?, ?, ?, 1)
		ON CONFLICT(account, asin) DO UPDATE SET
			want_ms = CASE WHEN library_id = excluded.library_id AND path = excluded.path THEN want_ms ELSE NULL END,
			library_id = excluded.library_id, path = excluded.path, runtime_s = excluded.runtime_s, listed = 1`)
	if err != nil {
		return err
	}
	defer func() { _ = upsert.Close() }()
	for _, t := range titles {
		if _, err := upsert.ExecContext(ctx, account, t.ASIN, t.LibraryID, t.Path, t.RuntimeS); err != nil {
			return err
		}
	}
	if _, err := tx.ExecContext(ctx, `UPDATE audible_push SET want_ms = NULL WHERE account = ? AND listed = 0`, account); err != nil {
		return err
	}
	return tx.Commit()
}

// AudiblePushes lists account's rows, by ASIN.
func (db *DB) AudiblePushes(ctx context.Context, account string) ([]AudiblePush, error) {
	return db.audiblePushes(ctx, `SELECT `+audiblePushColumns+` FROM audible_push WHERE account = ? ORDER BY asin`, account)
}

// AudibleWaiting lists account's listed rows with a place waiting to be
// pushed, by ASIN.
func (db *DB) AudibleWaiting(ctx context.Context, account string) ([]AudiblePush, error) {
	return db.audiblePushes(ctx, `SELECT `+audiblePushColumns+` FROM audible_push
		WHERE account = ? AND listed = 1 AND want_ms IS NOT NULL ORDER BY asin`, account)
}

func (db *DB) audiblePushes(ctx context.Context, query, account string) ([]AudiblePush, error) {
	rows, err := db.sql.QueryContext(ctx, query, account)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	out := []AudiblePush{}
	for rows.Next() {
		p, err := scanAudiblePush(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

// GetAudiblePush returns one row, or ErrNotFound.
func (db *DB) GetAudiblePush(ctx context.Context, account, asin string) (AudiblePush, error) {
	p, err := scanAudiblePush(db.sql.QueryRowContext(ctx, `SELECT `+audiblePushColumns+` FROM audible_push
		WHERE account = ? AND asin = ?`, account, asin))
	if errors.Is(err, sql.ErrNoRows) {
		return AudiblePush{}, ErrNotFound
	}
	return p, err
}

// WantAudiblePush records a server place waiting to be pushed: ms, from the
// server row updated at rowUpdated. Only a listed row takes one.
func (db *DB) WantAudiblePush(ctx context.Context, account, asin string, ms int64, rowUpdated time.Time) error {
	_, err := db.sql.ExecContext(ctx, `UPDATE audible_push SET want_ms = ?, row_updated = ? WHERE account = ? AND asin = ? AND listed = 1`,
		ms, timestamp(rowUpdated), account, asin)
	return err
}

// SeenAudibleRow records that the server row updated at rowUpdated was read
// and needs no push (an echo of Audible's own place, past the runtime, ...):
// the waiting place goes.
func (db *DB) SeenAudibleRow(ctx context.Context, account, asin string, rowUpdated time.Time) error {
	_, err := db.sql.ExecContext(ctx, `UPDATE audible_push SET want_ms = NULL, row_updated = ? WHERE account = ? AND asin = ?`,
		timestamp(rowUpdated), account, asin)
	return err
}

// DropAudiblePush forgets (account, asin)'s waiting place without a write.
func (db *DB) DropAudiblePush(ctx context.Context, account, asin string) error {
	_, err := db.sql.ExecContext(ctx, `UPDATE audible_push SET want_ms = NULL WHERE account = ? AND asin = ?`, account, asin)
	return err
}

// AudiblePushed records that ms was written to Audible at at (the waiting
// place goes).
func (db *DB) AudiblePushed(ctx context.Context, account, asin string, ms int64, at time.Time) error {
	_, err := db.sql.ExecContext(ctx, `UPDATE audible_push SET want_ms = NULL, last_ms = ?, at = ? WHERE account = ? AND asin = ?`,
		ms, timestamp(at), account, asin)
	return err
}

// AudibleHeard records the Audible place an audible.sync sent to the server
// for (account, asin).
func (db *DB) AudibleHeard(ctx context.Context, account, asin string, ms int64) error {
	_, err := db.sql.ExecContext(ctx, `UPDATE audible_push SET heard_ms = ? WHERE account = ? AND asin = ?`, ms, account, asin)
	return err
}

// SetAudibleACR caches (account, asin)'s content reference ("" forgets it).
func (db *DB) SetAudibleACR(ctx context.Context, account, asin, acr string) error {
	_, err := db.sql.ExecContext(ctx, `UPDATE audible_push SET acr = ? WHERE account = ? AND asin = ?`, acr, account, asin)
	return err
}

// SetAudiblePushCursor records the push loop's place in id's progress feed,
// and from when it pushes (from is kept once set: "" leaves it).
func (db *DB) SetAudiblePushCursor(ctx context.Context, id, cursor string, from time.Time) error {
	_, err := db.sql.ExecContext(ctx, `UPDATE audible_accounts SET push_cursor = ?,
		push_from = CASE WHEN push_from = '' THEN ? ELSE push_from END WHERE id = ?`, cursor, timestamp(from), id)
	return err
}

// AudibleSyncOK records that id's sync (a job or a push) went through at at:
// its error and any wait clear.
func (db *DB) AudibleSyncOK(ctx context.Context, id string, at time.Time) error {
	_, err := db.sql.ExecContext(ctx, `UPDATE audible_accounts SET sync_at = ?, sync_error = '', sync_retry_at = '' WHERE id = ?`,
		timestamp(at), id)
	return err
}

// AudibleSyncFailed records why id's sync failed, and until when Audible asked
// the Bridge to wait (zero: no wait). It never refuses the account.
func (db *DB) AudibleSyncFailed(ctx context.Context, id, why string, retryAt time.Time) error {
	retry := ""
	if !retryAt.IsZero() {
		retry = timestamp(retryAt)
	}
	_, err := db.sql.ExecContext(ctx, `UPDATE audible_accounts SET sync_error = ?, sync_retry_at = ? WHERE id = ?`, why, retry, id)
	return err
}
