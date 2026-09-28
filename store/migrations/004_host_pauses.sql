CREATE TABLE host_pauses (
    host TEXT PRIMARY KEY,
    paused_until TEXT NOT NULL,
    reason TEXT NOT NULL DEFAULT ''
);
