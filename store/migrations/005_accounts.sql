-- journal: foreign_keys=off
CREATE TABLE users_new (
    id INTEGER PRIMARY KEY,
    handle TEXT NOT NULL UNIQUE,
    display_name TEXT NOT NULL DEFAULT '',
    is_admin INTEGER NOT NULL DEFAULT 0,
    ui TEXT NOT NULL DEFAULT 'classic',
    kdf_salt BLOB,
    kdf_params TEXT,
    dek_wrapped BLOB,
    migrated_at TEXT,
    created_at TEXT NOT NULL
);

INSERT INTO users_new (id, handle, display_name, is_admin, ui, migrated_at, created_at)
SELECT id, lj_username, display_name, CASE WHEN id = 1 THEN 1 ELSE 0 END, 'classic', migrated_at, created_at
FROM users;

CREATE TABLE accounts (
    id INTEGER PRIMARY KEY,
    user_id INTEGER NOT NULL,
    service TEXT NOT NULL,
    username TEXT NOT NULL,
    secret_scheme TEXT NOT NULL DEFAULT 'legacy',
    key_id BLOB,
    password_enc BLOB,
    session_enc BLOB,
    sync_status TEXT NOT NULL DEFAULT 'ok' CHECK (sync_status IN ('ok', 'auth_failed', 'blocked', 'error')),
    sync_error TEXT NOT NULL DEFAULT '',
    sync_fail_count INTEGER NOT NULL DEFAULT 0,
    last_synced_at TEXT,
    friends_synced_at TEXT,
    blocked_until TEXT,
    next_sync_at TEXT,
    walk_skip INTEGER NOT NULL DEFAULT 0,
    walk_generation INTEGER NOT NULL DEFAULT 1,
    walk_oldest_seen TEXT,
    UNIQUE (user_id, service),
    UNIQUE (service, username)
);

INSERT INTO accounts (
    user_id, service, username, secret_scheme, password_enc, session_enc,
    sync_status, sync_error, sync_fail_count, last_synced_at, friends_synced_at,
    blocked_until, next_sync_at, walk_skip)
SELECT id, 'livejournal', lj_username, 'legacy', lj_pw_md5_enc, lj_session_enc,
    sync_status, sync_error, sync_fail_count, last_synced_at, friends_synced_at,
    blocked_until, next_sync_at, friends_backfill_skip
FROM users;

DROP TABLE users;
ALTER TABLE users_new RENAME TO users;

CREATE TABLE entries_new (
    id INTEGER PRIMARY KEY,
    service TEXT NOT NULL,
    origin TEXT NOT NULL,
    source TEXT NOT NULL CHECK (source IN ('remote', 'native')),
    author_username TEXT NOT NULL,
    journal_username TEXT NOT NULL,
    remote_id TEXT,
    url TEXT NOT NULL DEFAULT '',
    subject TEXT NOT NULL DEFAULT '',
    body_html TEXT NOT NULL DEFAULT '',
    security TEXT NOT NULL CHECK (security IN ('public', 'friends', 'private', 'custom')),
    allowmask INTEGER NOT NULL DEFAULT 0,
    event_time TEXT NOT NULL,
    userpic_url TEXT NOT NULL DEFAULT '',
    mood TEXT NOT NULL DEFAULT '',
    music TEXT NOT NULL DEFAULT '',
    comment_count INTEGER NOT NULL DEFAULT 0,
    journal_type TEXT NOT NULL DEFAULT '',
    last_seen_at TEXT,
    created_at TEXT NOT NULL,
    updated_at TEXT NOT NULL
);

INSERT INTO entries_new (
    id, service, origin, source, author_username, journal_username, remote_id, url,
    subject, body_html, security, allowmask, event_time, userpic_url, mood, music,
    comment_count, journal_type, last_seen_at, created_at, updated_at)
SELECT id,
    CASE WHEN source = 'lj' THEN 'livejournal' ELSE 'local' END,
    CASE WHEN source = 'lj' THEN 'friendspage' ELSE 'native' END,
    CASE WHEN source = 'lj' THEN 'remote' ELSE 'native' END,
    author_lj_username, journal_lj_username,
    CASE WHEN lj_itemid IS NULL THEN NULL ELSE CAST(lj_itemid AS TEXT) END,
    lj_url, subject, body_html, security, allowmask, event_time, userpic_url, mood, music,
    lj_comment_count, journal_type, updated_at, created_at, updated_at
FROM entries;

DROP TABLE entries;
ALTER TABLE entries_new RENAME TO entries;

CREATE UNIQUE INDEX idx_entries_remote ON entries(service, journal_username, remote_id) WHERE source = 'remote' AND remote_id IS NOT NULL;
CREATE INDEX idx_entries_event ON entries(event_time DESC, id DESC);
CREATE INDEX idx_entries_journal ON entries(journal_username, event_time DESC, id DESC);
CREATE INDEX idx_entries_author ON entries(author_username);

CREATE TABLE remote_friends (
    account_id INTEGER NOT NULL REFERENCES accounts(id) ON DELETE CASCADE,
    username TEXT NOT NULL,
    groupmask INTEGER NOT NULL DEFAULT 1,
    synced_at TEXT NOT NULL,
    PRIMARY KEY (account_id, username)
);

INSERT INTO remote_friends (account_id, username, groupmask, synced_at)
SELECT a.id, f.friend_lj_username, f.groupmask, f.synced_at
FROM lj_friends f
JOIN accounts a ON a.user_id = f.user_id AND a.service = 'livejournal';

DROP TABLE lj_friends;

ALTER TABLE friend_groups ADD COLUMN account_id INTEGER REFERENCES accounts(id) ON DELETE CASCADE;
UPDATE friend_groups SET account_id = (
    SELECT a.id FROM accounts a WHERE a.user_id = friend_groups.user_id AND a.service = 'livejournal'
) WHERE origin = 'lj';

ALTER TABLE entry_visibility ADD COLUMN account_id INTEGER REFERENCES accounts(id) ON DELETE CASCADE;
ALTER TABLE entry_visibility ADD COLUMN walk_generation INTEGER NOT NULL DEFAULT 1;
ALTER TABLE entry_visibility ADD COLUMN last_seen_at TEXT;
UPDATE entry_visibility SET
    account_id = (SELECT a.id FROM accounts a WHERE a.user_id = entry_visibility.viewer_user_id AND a.service = 'livejournal'),
    last_seen_at = (SELECT e.updated_at FROM entries e WHERE e.id = entry_visibility.entry_id);

CREATE TABLE subscriptions (
    user_id INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    service TEXT NOT NULL,
    journal TEXT NOT NULL,
    created_at TEXT NOT NULL,
    PRIMARY KEY (user_id, service, journal)
);

CREATE TABLE public_feeds (
    id INTEGER PRIMARY KEY,
    service TEXT NOT NULL,
    journal TEXT NOT NULL,
    feed_url TEXT NOT NULL,
    last_fetched_at TEXT,
    last_error TEXT NOT NULL DEFAULT '',
    etag TEXT NOT NULL DEFAULT '',
    last_modified TEXT NOT NULL DEFAULT '',
    next_fetch_at TEXT,
    UNIQUE (service, feed_url)
);

CREATE TABLE suppressions (
    user_id INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    service TEXT NOT NULL,
    username TEXT NOT NULL,
    created_at TEXT NOT NULL,
    PRIMARY KEY (user_id, service, username)
);

CREATE INDEX idx_suppressions_user ON suppressions(user_id, service, username);

CREATE TABLE invites (
    id INTEGER PRIMARY KEY,
    token_hash TEXT NOT NULL UNIQUE,
    admin INTEGER NOT NULL DEFAULT 0,
    created_by INTEGER REFERENCES users(id),
    created_at TEXT NOT NULL,
    expires_at TEXT NOT NULL,
    used_at TEXT,
    used_by INTEGER REFERENCES users(id)
);

CREATE TABLE recovery_tokens (
    id INTEGER PRIMARY KEY,
    user_id INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    token_hash TEXT NOT NULL UNIQUE,
    created_at TEXT NOT NULL,
    expires_at TEXT NOT NULL,
    used_at TEXT
);
