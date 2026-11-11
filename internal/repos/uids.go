package repos

import "jidoka/internal/db"

// JobUID returns the uid of job; a new job gets the next free one from
// base up. Never reused: a removed job keeps its row (Z3).
//
// Call inside a write transaction so two new jobs cannot get one uid.
func JobUID(q db.Queryer, job string, base uint32) (uint32, error) {
	uid, err := queryOne(q, func(s scanner) (uint32, error) {
		var n uint32
		err := s.Scan(&n)
		return n, err
	}, "SELECT uid FROM job_uids WHERE job = ?", job)
	if err == nil {
		return uid, nil
	}
	if err != ErrNotFound {
		return 0, err
	}

	var top uint32
	if err := q.QueryRow("SELECT COALESCE(MAX(uid), 0) FROM job_uids").Scan(&top); err != nil {
		return 0, err
	}

	uid = max(top+1, base)
	_, err = q.Exec("INSERT INTO job_uids (job, uid) VALUES (?, ?)", job, uid)

	return uid, err
}

// JobUIDs returns the uid of every job that has one; unlike JobUID it
// assigns none.
func JobUIDs(q db.Queryer) (map[string]uint32, error) {
	type row struct {
		job string
		uid uint32
	}

	list, err := queryAll(q, func(s scanner) (row, error) {
		var r row
		err := s.Scan(&r.job, &r.uid)
		return r, err
	}, "SELECT job, uid FROM job_uids")
	if err != nil {
		return nil, err
	}

	out := make(map[string]uint32, len(list))
	for _, r := range list {
		out[r.job] = r.uid
	}

	return out, nil
}
