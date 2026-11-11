package repos

import (
	"testing"
	"time"
)

func TestAudit(t *testing.T) {
	d := testDB(t)
	must(t, AddAudit(d, "arian", "set", "DAVIS_PASSWORD", t0))
	must(t, AddAudit(d, "ines", "delete", "DAVIS_PASSWORD", t0.Add(time.Minute)))

	got, err := RecentAudit(d, 1)
	must(t, err)
	if len(got) != 1 || got[0].Actor != "ines" || got[0].Action != "delete" ||
		got[0].Target != "DAVIS_PASSWORD" || !got[0].At.Equal(t0.Add(time.Minute)) {
		t.Fatalf("recent: %+v", got)
	}
}
