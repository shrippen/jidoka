-- Times are UTC text (RFC 3339 with nanoseconds, db.TimeStr) (B3).

-- One run of a job or one ping of a watch.
CREATE TABLE runs (
    id         INTEGER PRIMARY KEY,
    job        TEXT    NOT NULL,
    trigger    TEXT    NOT NULL,
    state      TEXT    NOT NULL,
    slot       TEXT    NOT NULL,           -- scheduled time; retries share it (J13)
    attempt    INTEGER NOT NULL DEFAULT 1,
    started_at TEXT    NOT NULL,
    ended_at   TEXT,
    exit_code  INTEGER,
    summary    TEXT    NOT NULL DEFAULT '',
    message    TEXT    NOT NULL DEFAULT '',
    output     TEXT    NOT NULL DEFAULT '',
    error      TEXT    NOT NULL DEFAULT '',
    jobs_commit TEXT   NOT NULL DEFAULT '',
    dry        INTEGER NOT NULL DEFAULT 0
);
CREATE INDEX runs_job ON runs (job, id DESC);
CREATE INDEX runs_state ON runs (state);

-- What the UI sets on a job, keyed by id so it outlives a removal (J21).
CREATE TABLE flags (
    job          TEXT PRIMARY KEY,
    paused_at    TEXT,
    paused_by    TEXT NOT NULL DEFAULT '',
    acked_at     TEXT,
    muted_until  TEXT,
    mute_recover INTEGER NOT NULL DEFAULT 0
);

-- Per job: when Jidoka first saw it (W1 window start) and the last slot
-- the planner handled (catchup J10, no double fire after restart).
CREATE TABLE schedule (
    job        TEXT PRIMARY KEY,
    first_seen TEXT NOT NULL,
    last_slot  TEXT
);

-- Ongoing disturbance per job and what was already sent (M3, M5).
CREATE TABLE alerts (
    job       TEXT PRIMARY KEY,
    event     TEXT NOT NULL,
    since     TEXT NOT NULL,
    streak    INTEGER NOT NULL DEFAULT 0,  -- slots in a row with disturbance
    last_slot TEXT,                        -- slot counted last
    run_id    INTEGER,                     -- last run that belongs to it
    name      TEXT NOT NULL DEFAULT '',
    detail    TEXT NOT NULL DEFAULT '',
    sent      TEXT NOT NULL DEFAULT '{}'   -- JSON: rule index -> time of last message
);

-- Messages waiting: quiet hours, digest, retry, both channels down (M6, M7, M9).
CREATE TABLE outbox (
    id        INTEGER PRIMARY KEY,
    created   TEXT NOT NULL,
    due       TEXT NOT NULL,
    job       TEXT NOT NULL DEFAULT '',
    run_id    INTEGER,
    event     TEXT NOT NULL,
    channel   TEXT NOT NULL,
    digest    TEXT NOT NULL DEFAULT '',
    payload   TEXT NOT NULL,                -- JSON
    attempts  INTEGER NOT NULL DEFAULT 0,
    last_error TEXT NOT NULL DEFAULT ''
);
CREATE INDEX outbox_due ON outbox (due);

-- Sent messages, per channel (O4, O8).
CREATE TABLE deliveries (
    id      INTEGER PRIMARY KEY,
    job     TEXT NOT NULL DEFAULT '',
    run_id  INTEGER,
    event   TEXT NOT NULL,
    channel TEXT NOT NULL,
    at      TEXT NOT NULL,
    ok      INTEGER NOT NULL,
    error   TEXT NOT NULL DEFAULT ''
);
CREATE INDEX deliveries_at ON deliveries (at DESC);
CREATE INDEX deliveries_run ON deliveries (run_id);

-- Secrets of jobs and of Jidoka itself, AES-GCM (Z7).
CREATE TABLE secrets (
    name       TEXT PRIMARY KEY,
    value_enc  BLOB NOT NULL,
    updated_at TEXT NOT NULL,
    updated_by TEXT NOT NULL
);

-- Who changed what, never a value (O11).
CREATE TABLE audit (
    id     INTEGER PRIMARY KEY,
    at     TEXT NOT NULL,
    actor  TEXT NOT NULL,
    action TEXT NOT NULL,
    target TEXT NOT NULL
);

-- API tokens, hashed (Z2).
CREATE TABLE tokens (
    id         INTEGER PRIMARY KEY,
    name       TEXT NOT NULL,
    hash       TEXT NOT NULL UNIQUE,
    rights     TEXT NOT NULL,              -- comma list: read,run,reload
    created_at TEXT NOT NULL,
    last_used  TEXT
);

-- Ping tokens of watches, shown again on the page, so encrypted (W3).
CREATE TABLE pings (
    job        TEXT PRIMARY KEY,
    token_hash TEXT NOT NULL UNIQUE,
    token_enc  BLOB NOT NULL,
    created_at TEXT NOT NULL
);

-- Small state: last valid jidoka.toml (B8), planner tick (B5), salt check.
CREATE TABLE kv (
    key   TEXT PRIMARY KEY,
    value TEXT NOT NULL
);
