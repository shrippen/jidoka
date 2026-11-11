-- The disturbance as it was before a broken file turned it into
-- "invalid" (JSON, '' = none); the repair gives it back whole.
ALTER TABLE alerts ADD COLUMN prev TEXT NOT NULL DEFAULT '';
