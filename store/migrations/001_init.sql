CREATE TABLE users (
    id INTEGER PRIMARY KEY,
    lj_username TEXT NOT NULL UNIQUE,
    display_name TEXT NOT NULL DEFAULT '',
    lj_pw_md5_enc BLOB,
    lj_session_enc BLOB,
    migrated_at TEXT,
    sync_status TEXT NOT NULL DEFAULT 'ok' CHECK (sync_status IN ('ok', 'auth_failed', 'blocked', 'error')),
    sync_error TEXT NOT NULL DEFAULT '',
    sync_fail_count INTEGER NOT NULL DEFAULT 0,
    last_synced_at TEXT,
    friends_synced_at TEXT,
    blocked_until TEXT,
    next_sync_at TEXT,
    created_at TEXT NOT NULL
);

CREATE TABLE sessions (
    id TEXT PRIMARY KEY,
    user_id INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    expires_at TEXT NOT NULL
);

CREATE TABLE lj_friends (
    user_id INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    friend_lj_username TEXT NOT NULL,
    groupmask INTEGER NOT NULL DEFAULT 1,
    synced_at TEXT NOT NULL,
    PRIMARY KEY (user_id, friend_lj_username)
);

CREATE TABLE native_friends (
    user_id INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    friend_user_id INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    groupmask INTEGER NOT NULL DEFAULT 1,
    PRIMARY KEY (user_id, friend_user_id)
);

CREATE TABLE friend_groups (
    id INTEGER PRIMARY KEY,
    user_id INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    name TEXT NOT NULL,
    bit INTEGER NOT NULL CHECK (bit BETWEEN 1 AND 30),
    origin TEXT NOT NULL DEFAULT 'native' CHECK (origin IN ('lj', 'native')),
    UNIQUE (user_id, bit),
    UNIQUE (user_id, name)
);

CREATE TABLE entries (
    id INTEGER PRIMARY KEY,
    source TEXT NOT NULL CHECK (source IN ('lj', 'native')),
    author_lj_username TEXT NOT NULL,
    journal_lj_username TEXT NOT NULL,
    lj_itemid INTEGER,
    lj_url TEXT NOT NULL DEFAULT '',
    subject TEXT NOT NULL DEFAULT '',
    body_html TEXT NOT NULL DEFAULT '',
    security TEXT NOT NULL CHECK (security IN ('public', 'friends', 'private', 'custom')),
    allowmask INTEGER NOT NULL DEFAULT 0,
    event_time TEXT NOT NULL,
    userpic_url TEXT NOT NULL DEFAULT '',
    mood TEXT NOT NULL DEFAULT '',
    music TEXT NOT NULL DEFAULT '',
    lj_comment_count INTEGER NOT NULL DEFAULT 0,
    journal_type TEXT NOT NULL DEFAULT '',
    created_at TEXT NOT NULL,
    updated_at TEXT NOT NULL
);

CREATE UNIQUE INDEX idx_entries_lj ON entries(journal_lj_username, lj_itemid) WHERE source = 'lj';

CREATE INDEX idx_entries_event ON entries(event_time DESC, id DESC);
CREATE INDEX idx_entries_journal ON entries(journal_lj_username, event_time DESC, id DESC);
CREATE INDEX idx_entries_author ON entries(author_lj_username);

CREATE TABLE entry_visibility (
    entry_id INTEGER NOT NULL REFERENCES entries(id) ON DELETE CASCADE,
    viewer_user_id INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    PRIMARY KEY (entry_id, viewer_user_id)
);

CREATE INDEX idx_visibility_viewer ON entry_visibility(viewer_user_id, entry_id);

CREATE TABLE comments (
    id INTEGER PRIMARY KEY,
    entry_id INTEGER NOT NULL REFERENCES entries(id) ON DELETE CASCADE,
    parent_id INTEGER REFERENCES comments(id) ON DELETE CASCADE,
    author_user_id INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    body_html TEXT NOT NULL,
    created_at TEXT NOT NULL,
    deleted INTEGER NOT NULL DEFAULT 0 CHECK (deleted IN (0, 1))
);

CREATE INDEX idx_comments_entry ON comments(entry_id, id);
