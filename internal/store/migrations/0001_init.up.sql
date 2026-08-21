CREATE TABLE functions (
    name            TEXT    PRIMARY KEY,
    image           TEXT    NOT NULL,
    env             TEXT    NOT NULL,
    memory_mb       INTEGER NOT NULL,
    timeout_sec     INTEGER NOT NULL,
    created_at      TEXT    NOT NULL,
    updated_at      TEXT    NOT NULL,
    last_invoked_at TEXT
);
