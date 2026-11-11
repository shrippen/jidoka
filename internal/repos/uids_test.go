package repos

import (
	"database/sql"
	"testing"

	"jidoka/internal/db"
)

func TestJobUID(t *testing.T) {
	d := testDB(t)
	const base = 20000

	get := func(job string) uint32 {
		t.Helper()
		var uid uint32
		err := db.WithTx(d, func(tx *sql.Tx) error {
			var err error
			uid, err = JobUID(tx, job, base)
			return err
		})
		if err != nil {
			t.Fatal(err)
		}
		return uid
	}

	a, b := get("a"), get("b")
	if a != base || b != base+1 {
		t.Errorf("a=%d b=%d", a, b)
	}
	if get("a") != a || get("b") != b {
		t.Error("uid changed on second call")
	}

	// A job that left and returns keeps its uid; a new one never takes it.
	if c := get("c"); c != base+2 {
		t.Errorf("c=%d", c)
	}
	if get("a") != a {
		t.Error("returning job changed uid")
	}
}
