-- One system user per job (Z3): the uid stays with the job id for good,
-- also when the folder goes away, so files of a returning job stay its own.
CREATE TABLE job_uids (
    job TEXT PRIMARY KEY,
    uid INTEGER NOT NULL UNIQUE
);
