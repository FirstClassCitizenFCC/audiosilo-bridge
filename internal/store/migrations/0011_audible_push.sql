-- Phase 4c (BRIDGE-PLAN.md Phase 4): Audible position sync both ways.
--
-- audible_push is one row per (account, ASIN) the server's last audible.sync
-- job listed (listed = 1; a title the console marks "Don't sync this book",
-- or one that lost its match, is listed 0 at the next job and never pushed):
-- the book it matched (library_id, path) and Audible's runtime (runtime_s,
-- the past-runtime guard), the Audible content reference a write needs (acr,
-- cached: a license request per ASIN, not per push), the place last written
-- to Audible (last_ms, -1 none) and when (at), the Audible place the last sync
-- sent to the server (heard_ms, -1 none: a server row at that place is
-- Audible's own coming back, never pushed), the place waiting to be pushed
-- (want_ms, NULL none), and the server row last read for the title: its
-- version, place (row_ms, -1 none) and book length (row_duration, seconds, 0
-- unknown), and when this Bridge first read that place (row_seen, the
-- Bridge's clock: a place is pushed once it has been still for 3 minutes; the
-- feed pages in write order, so a row's updated_at says nothing about when it
-- changed here). No credential, no title.
CREATE TABLE audible_push (
    account      TEXT NOT NULL,
    asin         TEXT NOT NULL,
    library_id   INTEGER NOT NULL DEFAULT 0,
    path         TEXT NOT NULL DEFAULT '',
    runtime_s    INTEGER NOT NULL DEFAULT 0,
    listed       INTEGER NOT NULL DEFAULT 0,
    acr          TEXT NOT NULL DEFAULT '',
    last_ms      INTEGER NOT NULL DEFAULT -1,
    at           TEXT NOT NULL DEFAULT '',
    heard_ms     INTEGER NOT NULL DEFAULT -1,
    want_ms      INTEGER,
    row_version  INTEGER NOT NULL DEFAULT -1,
    row_ms       INTEGER NOT NULL DEFAULT -1,
    row_duration REAL NOT NULL DEFAULT 0,
    row_seen     TEXT NOT NULL DEFAULT '',
    PRIMARY KEY (account, asin)
);

-- The push loop's place in the server's progress feed (GET /bridge/progress):
-- push_cursor the feed's next ('' from the start), push_from the moment the
-- loop first read the feed to its end for the account's mapping ('' while
-- that first read, the baseline, is under way: only places changed in its last
-- minutes are pushed, the rest is history and stays where it is). Both start
-- again when the account is mapped to another server, person or library.
ALTER TABLE audible_accounts ADD COLUMN push_cursor TEXT NOT NULL DEFAULT '';
ALTER TABLE audible_accounts ADD COLUMN push_from TEXT NOT NULL DEFAULT '';

-- The sync's health, for the setup item's detail: sync_at when a sync job or
-- a push last went through, sync_error why the last one failed ('' when it
-- went through), sync_retry_at until when Audible asked the Bridge to wait (a
-- 429/503's Retry-After). A sync error never refuses the account.
ALTER TABLE audible_accounts ADD COLUMN sync_at TEXT NOT NULL DEFAULT '';
ALTER TABLE audible_accounts ADD COLUMN sync_error TEXT NOT NULL DEFAULT '';
ALTER TABLE audible_accounts ADD COLUMN sync_retry_at TEXT NOT NULL DEFAULT '';

-- bridge#46: a refusal's kind in a column, instead of matching last_error's
-- text: 'sign_in' (Audible refused the sign-in: any answered call ends it) or
-- 'device' (Audible denied the device its licenses, a device on an older
-- profile: only a sign-in again ends it); '' while not refused. A refused row
-- whose last_error is the throttled text (audibleacct.ThrottledText as 4b
-- stored it) is a device refusal, any other refused row a sign-in one.
ALTER TABLE audible_accounts ADD COLUMN refusal_kind TEXT NOT NULL DEFAULT '';
UPDATE audible_accounts SET refusal_kind = 'device'
    WHERE refused_at <> '' AND last_error = 'Audible refused this device its downloads (customer throttled): sign in again';
UPDATE audible_accounts SET refusal_kind = 'sign_in' WHERE refused_at <> '' AND refusal_kind = '';
