-- Browser sessions, by the hash of the cookie token, so a restart keeps
-- the login (Z1). Sliding: expires moves with use.
CREATE TABLE sessions (
    hash      TEXT PRIMARY KEY,
    sub       TEXT NOT NULL,
    name      TEXT NOT NULL,
    created   TEXT NOT NULL,
    last_seen TEXT NOT NULL,
    expires   TEXT NOT NULL
);
CREATE INDEX sessions_expires ON sessions (expires);
