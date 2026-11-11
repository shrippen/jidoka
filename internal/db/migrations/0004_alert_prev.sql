-- The kind a disturbance had before a broken file turned it into "invalid";
-- the repair gives it back instead of reporting a recovery.
ALTER TABLE alerts ADD COLUMN prev_event TEXT NOT NULL DEFAULT '';
