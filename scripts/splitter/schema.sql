CREATE TABLE IF NOT EXISTS songs (
    [id] TEXT NOT NULL PRIMARY KEY,
    [path] TEXT NOT NULL,
    [updated_at] TEXT NOT NULL DEFAULT (datetime('now')),
    [created_at] TEXT NOT NULL DEFAULT (datetime('now'))
) STRICT;

CREATE TABLE IF NOT EXISTS patterns (
    [hash] TEXT NOT NULL PRIMARY KEY,
    [title] TEXT,
    [subtitle] TEXT,
    [artist] TEXT,
    [subartist] TEXT,
    [path] TEXT NOT NULL,
    [song_id] TEXT NOT NULL,
    [updated_at] TEXT NOT NULL DEFAULT (datetime('now')),
    [created_at] TEXT NOT NULL DEFAULT (datetime('now'))
) STRICT;
