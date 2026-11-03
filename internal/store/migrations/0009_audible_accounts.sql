-- Phase 4a (BRIDGE-PLAN.md Phase 4): the Audible accounts the Bridge signed in to.
--
-- One row per account (the Accounts tab's Audible section). The sign-in itself
-- (tokens and the device key: full access to the Amazon account) is sealed in
-- the vault as audible_<id>, never here. id is the Bridge's own opaque id
-- (^[a-z0-9]{8,32}$, the server's account key); customer_hash the sha256 of the
-- Amazon customer id, so a second sign-in of the same Amazon account replaces
-- the first rather than adding a device beside it; server_id the server the
-- mapping (user_id, user_name, library_id, policy, sync) was last recorded on.
-- last_ok_at is when Audible last answered; refused_at when it last refused
-- the sign-in ('' while it works) and last_error why a poll last failed, in
-- words; last_titles and last_matched count the last complete poll.
CREATE TABLE audible_accounts (
    id            TEXT PRIMARY KEY,
    label         TEXT NOT NULL,
    marketplace   TEXT NOT NULL,
    customer_hash TEXT NOT NULL DEFAULT '',
    server_id     TEXT NOT NULL DEFAULT '',
    user_id       INTEGER NOT NULL DEFAULT 0,
    user_name     TEXT NOT NULL DEFAULT '',
    library_id    INTEGER NOT NULL DEFAULT 0,
    policy        TEXT NOT NULL DEFAULT 'manual',
    sync          INTEGER NOT NULL DEFAULT 1,
    created_at    TEXT NOT NULL,
    last_ok_at    TEXT NOT NULL DEFAULT '',
    last_poll_at  TEXT NOT NULL DEFAULT '',
    last_titles   INTEGER NOT NULL DEFAULT 0,
    last_matched  INTEGER NOT NULL DEFAULT 0,
    refused_at    TEXT NOT NULL DEFAULT '',
    last_error    TEXT NOT NULL DEFAULT ''
);
-- One account per Amazon account and store: a second sign-in replaces the
-- first (audibleacct.Finish, which also serialises sign-ins). An account whose
-- customer id Amazon didn't send ('') is never matched, so it is left out.
CREATE UNIQUE INDEX audible_accounts_customer ON audible_accounts(customer_hash, marketplace)
    WHERE customer_hash <> '';
