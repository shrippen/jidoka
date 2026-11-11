package repos

import (
	"testing"
	"time"

	"jidoka/internal/model"
)

func TestDeliveries(t *testing.T) {
	d := testDB(t)
	run := int64(4)
	_, err := AddDelivery(d, model.Delivery{Job: "a", RunID: &run, Event: model.EvFail,
		Channel: "apprise", At: t0, OK: true})
	must(t, err)
	_, err = AddDelivery(d, model.Delivery{Job: "a", Event: model.EvFail, Channel: "mail",
		At: t0.Add(time.Minute), Error: "refused"})
	must(t, err)
	_, err = AddDelivery(d, model.Delivery{Job: "b", Event: model.EvRecover, Channel: "mail",
		At: t0.Add(2 * time.Minute), OK: true})
	must(t, err)

	byRun, err := DeliveriesByRun(d, 4)
	must(t, err)
	if len(byRun) != 1 || !byRun[0].OK || byRun[0].RunID == nil || *byRun[0].RunID != 4 {
		t.Fatalf("by run: %+v", byRun)
	}

	recent, err := RecentDeliveries(d, 2)
	must(t, err)
	if len(recent) != 2 || recent[0].Job != "b" || recent[1].Error != "refused" {
		t.Fatalf("recent: %+v", recent)
	}

	last, err := LastDeliveries(d)
	must(t, err)
	if len(last) != 2 || last["mail"].Job != "b" || !last["mail"].OK || last["apprise"].Event != model.EvFail {
		t.Fatalf("last: %+v", last)
	}
}

func TestDeliveryKeepsNote(t *testing.T) {
	d := testDB(t)
	_, err := AddDelivery(d, model.Delivery{Job: "a", Event: model.EvRecover, Channel: "mail",
		At: t0, OK: true, Note: "alert.file_fixed"})
	must(t, err)

	recent, err := RecentDeliveries(d, 1)
	must(t, err)
	if len(recent) != 1 || recent[0].Note != "alert.file_fixed" {
		t.Fatalf("note lost: %+v", recent)
	}
}
