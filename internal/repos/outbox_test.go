package repos

import (
	"testing"
	"time"

	"jidoka/internal/model"
)

func TestOutbox(t *testing.T) {
	d := testDB(t)
	run := int64(7)
	first, err := AddOutbox(d, OutboxItem{Created: t0, Due: t0, Job: "a", RunID: &run,
		Event: model.EvFail, Channel: "apprise", Payload: `{"x":1}`})
	must(t, err)
	_, err = AddOutbox(d, OutboxItem{Created: t0, Due: t0.Add(time.Hour), Job: "a",
		Event: model.EvSuccess, Channel: "mail", Digest: "daily", Payload: "{}"})
	must(t, err)

	due, err := DueOutbox(d, t0.Add(time.Minute))
	must(t, err)
	if len(due) != 1 || due[0].ID != first || due[0].RunID == nil || *due[0].RunID != 7 ||
		due[0].Payload != `{"x":1}` {
		t.Fatalf("due: %+v", due)
	}

	next := t0.Add(5 * time.Minute)
	must(t, AttemptOutbox(d, first, "smtp down", next))
	if due, _ = DueOutbox(d, t0.Add(time.Minute)); len(due) != 0 {
		t.Fatal("still due after attempt")
	}

	all, _ := AllOutbox(d)
	if len(all) != 2 || all[0].Attempts != 1 || all[0].LastError != "smtp down" || !all[0].Due.Equal(next) {
		t.Fatalf("all: %+v", all)
	}
	if mail, _ := OutboxByChannel(d, "mail"); len(mail) != 1 || mail[0].Digest != "daily" || mail[0].RunID != nil {
		t.Fatalf("by channel: %+v", mail)
	}

	must(t, DeleteOutbox(d, first))
	if all, _ = AllOutbox(d); len(all) != 1 {
		t.Fatalf("after delete: %+v", all)
	}
}
