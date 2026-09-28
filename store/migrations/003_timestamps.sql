-- Rewrite every stored timestamp to fixed-width UTC text.
-- SQLite %f yields milliseconds, matching store.FormatTime.

UPDATE users SET
    migrated_at = strftime('%Y-%m-%dT%H:%M:%fZ', migrated_at),
    last_synced_at = strftime('%Y-%m-%dT%H:%M:%fZ', last_synced_at),
    friends_synced_at = strftime('%Y-%m-%dT%H:%M:%fZ', friends_synced_at),
    blocked_until = strftime('%Y-%m-%dT%H:%M:%fZ', blocked_until),
    next_sync_at = strftime('%Y-%m-%dT%H:%M:%fZ', next_sync_at),
    created_at = strftime('%Y-%m-%dT%H:%M:%fZ', created_at);

UPDATE sessions SET expires_at = strftime('%Y-%m-%dT%H:%M:%fZ', expires_at);

UPDATE lj_friends SET synced_at = strftime('%Y-%m-%dT%H:%M:%fZ', synced_at);

UPDATE entries SET
    event_time = strftime('%Y-%m-%dT%H:%M:%fZ', event_time),
    created_at = strftime('%Y-%m-%dT%H:%M:%fZ', created_at),
    updated_at = strftime('%Y-%m-%dT%H:%M:%fZ', updated_at);

UPDATE comments SET created_at = strftime('%Y-%m-%dT%H:%M:%fZ', created_at);

UPDATE schema_migrations SET applied_at = strftime('%Y-%m-%dT%H:%M:%fZ', applied_at);
