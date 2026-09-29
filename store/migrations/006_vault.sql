-- journal: foreign_keys=off
ALTER TABLE accounts ADD COLUMN secret_state TEXT NOT NULL DEFAULT '';
ALTER TABLE accounts ADD COLUMN remember_password INTEGER NOT NULL DEFAULT 1;

CREATE TABLE comments_new (
    id INTEGER PRIMARY KEY,
    entry_id INTEGER NOT NULL REFERENCES entries(id) ON DELETE CASCADE,
    parent_id INTEGER REFERENCES comments(id) ON DELETE CASCADE,
    author_user_id INTEGER REFERENCES users(id) ON DELETE SET NULL,
    body_html TEXT NOT NULL,
    created_at TEXT NOT NULL,
    deleted INTEGER NOT NULL DEFAULT 0 CHECK (deleted IN (0, 1))
);

INSERT INTO comments_new (id, entry_id, parent_id, author_user_id, body_html, created_at, deleted)
SELECT id, entry_id, parent_id, author_user_id, body_html, created_at, deleted FROM comments;

DROP TABLE comments;
ALTER TABLE comments_new RENAME TO comments;
CREATE INDEX idx_comments_entry ON comments(entry_id, id);
