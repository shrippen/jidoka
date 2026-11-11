-- The settings live in the database, not in jidoka.toml of the jobs repo.
-- The last valid copy of that file becomes the stored document.
UPDATE kv SET key = 'settings' WHERE key = 'jobs.settings_valid';
