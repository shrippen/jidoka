-- State word of a delivery that is not a plain event, e.g. a repaired file
-- of a job that still fails (catalog key, '' = none).
ALTER TABLE deliveries ADD COLUMN note TEXT NOT NULL DEFAULT '';
