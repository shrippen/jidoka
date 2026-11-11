package web

import (
	"strings"
	"testing"
	"time"

	"jidoka/internal/model"
	"jidoka/internal/repos"
)

// A configured channel with no delivery is "not checked", never
// "reachable": reachability is unknown until a send went through.
func TestChannelUnchecked(t *testing.T) {
	b, s := seeded(t)
	err := repos.PutSettings(b.db, "[channels.mail]\nsmtp = \"h:25\"\nfrom = \"a@b\"\nto = [\"c@d\"]\n\n"+
		"[[profiles.standard]]\non = [\"fail\"]\nchannels = [\"mail\"]\n")
	if err != nil {
		t.Fatal(err)
	}
	if err := b.jobs.Load(); err != nil {
		t.Fatal(err)
	}

	page := func() string {
		return do(b.srv, "GET", "/channels", nil, s.cookies, map[string]string{"Accept-Language": "en"}).Body.String()
	}

	body := page()
	if !strings.Contains(body, "not checked") {
		t.Errorf("no delivery: %q missing", "not checked")
	}
	if strings.Contains(body, "reachable") {
		t.Errorf("no delivery: page says reachable")
	}

	_, err = repos.AddDelivery(b.db, model.Delivery{Job: "bad", Event: model.EvFail,
		Channel: "mail", At: time.Now(), OK: true})
	if err != nil {
		t.Fatal(err)
	}

	body = page()
	if !strings.Contains(body, "reachable") || strings.Contains(body, "not checked") {
		t.Errorf("after a sent message: want reachable, not %q", "not checked")
	}
}
