package conf

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"jidoka/internal/i18n"
	"jidoka/internal/model"
)

const testdataFull = "testdata/full"

// write builds a jobs dir from path -> content.
func write(t *testing.T, files map[string]string) string {
	t.Helper()

	dir := t.TempDir()
	for name, content := range files {
		p := filepath.Join(dir, name)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	return dir
}

const okSettings = `
[channels.apprise]
url = "http://apprise:8000/notify"
tags = { handy = "handy" }

[[profiles.standard]]
on = ["fail"]
channels = ["apprise"]

[[profiles.leise]]
on = ["fail"]
channels = ["mail"]
`

func TestLoadFull(t *testing.T) {
	set := Load(testdataFull, nil)

	if set.SettingsErr != nil {
		t.Fatalf("SettingsErr: %+v", set.SettingsErr)
	}
	if len(set.Invalid) != 0 {
		t.Fatalf("Invalid: %+v", set.Invalid)
	}
	if len(set.Jobs) != 2 {
		t.Fatalf("want 2 jobs, got %d", len(set.Jobs))
	}

	s := set.Settings
	if s.Zone.String() != "Europe/Berlin" || s.Heartbeat != 5*time.Minute || s.MaxParallel != 2 ||
		s.KeepRuns != 500 || s.Keep != 180*24*time.Hour {
		t.Errorf("settings: %+v", s)
	}
	if s.Quiet == nil || s.Quiet.Start != 22*time.Hour || s.Quiet.End != 7*time.Hour {
		t.Errorf("quiet: %+v", s.Quiet)
	}
	if s.Apprise.Tags["desktop"] != "kde" || s.Mail.From != "jidoka@arianw.de" {
		t.Errorf("channels: %+v %+v", s.Apprise, s.Mail)
	}
	if len(s.Profiles["standard"]) != 2 || s.Profiles["leise"][0].After != 3 {
		t.Errorf("profiles: %+v", s.Profiles)
	}
	if r := s.Profiles["standard"][0]; r.Remind != 24*time.Hour || r.Tail != -1 {
		t.Errorf("profile rule: %+v", r)
	}

	j := set.Jobs["shinkiryu"]
	if j == nil {
		t.Fatal("job shinkiryu missing")
	}
	if j.Kind != model.KindJob || j.Name != "Shinkiryu-Lehrgänge → Kalender" || j.Run != "main.py" ||
		j.Schedule != "0 6 * * *" || j.Jitter != 5*time.Minute || j.Timeout != 5*time.Minute ||
		j.Slow != 2*time.Minute || j.Retries != 2 || !j.Catchup || j.Lock != "davis" ||
		j.Expect != 26*time.Hour || j.Keep != 365*24*time.Hour || !j.Enabled ||
		j.Profile != "standard" || j.File != "shinkiryu/job.toml" {
		t.Errorf("job: %+v", j)
	}
	if j.Quiet == nil || j.Quiet.Zone.String() != "Europe/Berlin" {
		t.Errorf("job quiet: %+v", j.Quiet)
	}
	if len(j.Secrets) != 2 || j.Env["DAVIS_URL"] == "" || len(j.Args) != 2 || len(j.Tags) != 2 {
		t.Errorf("job lists: %+v", j)
	}
	if !filepath.IsAbs(j.Dir) || len(j.Hash) != 64 {
		t.Errorf("dir/hash: %q %q", j.Dir, j.Hash)
	}

	if len(j.Rules) != 3 {
		t.Fatalf("rules: %+v", j.Rules)
	}
	r0, r2 := j.Rules[0], j.Rules[2]
	if r0.After != 2 || !r0.Urgent || r0.Remind != 24*time.Hour || len(r0.On) != 3 || r0.Tail != -1 {
		t.Errorf("rule 0: %+v", r0)
	}
	if r2.Digest != "weekly Mon 08:00" || r2.Tail != 0 || len(r2.To) != 1 {
		t.Errorf("rule 2: %+v", r2)
	}

	w := set.Jobs["borg"]
	if w == nil || w.Kind != model.KindWatch || w.Expect != 26*time.Hour || w.Period != 24*time.Hour ||
		w.Grace != 2*time.Hour || w.Profile != "leise" {
		t.Errorf("watch: %+v", w)
	}
}

func TestWatchScheduleExpect(t *testing.T) {
	dir := write(t, map[string]string{
		"w/watch.toml": "schedule = \"0 4 * * *\"\ngrace = \"1h\"\n",
	})

	set := Load(dir, nil)
	w := set.Jobs["w"]
	if w == nil {
		t.Fatalf("invalid: %+v", set.Invalid)
	}
	// Daily slot, 1 h grace; DST days may shift the gap by an hour.
	if w.Expect < 24*time.Hour || w.Expect > 26*time.Hour {
		t.Errorf("Expect = %v", w.Expect)
	}
}

func TestDefaults(t *testing.T) {
	dir := write(t, map[string]string{
		"a/job.toml": "run = \"x.sh\"\nschedule = \"*/5 * * * *\"\n",
		"a/x.sh":     "#!/bin/sh\n",
	})

	set := Load(dir, nil)
	j := set.Jobs["a"]
	if j == nil {
		t.Fatalf("invalid: %+v", set.Invalid)
	}
	if j.Timeout != 10*time.Minute || !j.Enabled || j.Profile != "standard" || j.Name != "a" ||
		j.Zone.String() != "Europe/Berlin" || j.Keep != 0 {
		t.Errorf("defaults: %+v", j)
	}
	if set.Settings.MaxParallel != 2 || set.Settings.Keep != 180*24*time.Hour {
		t.Errorf("settings defaults: %+v", set.Settings)
	}
	if set.LoadedAt.IsZero() || set.LoadedAt.Location() != time.UTC {
		t.Errorf("LoadedAt = %v", set.LoadedAt)
	}
}

// jobErr is one broken job.toml and the error expected.
type jobErr struct {
	name string
	toml string
	line int
	msg  string
}

// checkKey: an error carries a catalog key that exists, and its English
// Msg is that key's text (D2).
func checkKey(t *testing.T, e model.LoadError) {
	t.Helper()
	if e.Key == "" || !i18n.Has(e.Key) {
		t.Errorf("error without catalog key: %+v", e)
		return
	}
	if want := i18n.T(e.Key, i18n.LocaleEN, e.Params); e.Msg != want {
		t.Errorf("Msg = %q, want %q", e.Msg, want)
	}
	if de := i18n.T(e.Key, i18n.LocaleDE, e.Params); strings.Contains(de, "{") {
		t.Errorf("German text keeps a placeholder: %q", de)
	}
}

func TestJobErrors(t *testing.T) {
	const head = "run = \"x.sh\"\nschedule = \"0 6 * * *\"\n"

	tests := []jobErr{
		{"syntax", head + "name = \n", 3, ""},
		{"unknown key", head + "colour = \"red\"\n", 3, `unknown key "colour"`},
		{"unknown key in rule", head + "[[notify]]\non = [\"fail\"]\nchannels = [\"mail\"]\n\n[[notify]]\non = [\"fail\"]\nchannels = [\"mail\"]\nbogus = 1\n", 10, `unknown key "notify.bogus"`},
		{"no run", "schedule = \"0 6 * * *\"\n", 0, "run required"},
		{"run missing file", "run = \"nope.sh\"\nschedule = \"0 6 * * *\"\n", 1, "not found"},
		{"run escapes", "run = \"../x.sh\"\nschedule = \"0 6 * * *\"\n", 1, "inside the job folder"},
		{"no schedule", "run = \"x.sh\"\n", 0, "schedule required"},
		{"bad hour", "run = \"x.sh\"\nschedule = \"0 25 * * *\"\n", 2, "hour"},
		{"bad zone", head + "zone = \"Mars/Base\"\n", 3, "unknown zone"},
		{"bad duration", head + "timeout = \"soon\"\n", 3, "invalid duration"},
		{"zero timeout", head + "timeout = \"0s\"\n", 3, "timeout must be positive"},
		{"bad quiet", head + "quiet = \"22-7\"\n", 3, "invalid time"},
		{"bad secret", head + "secrets = [\"ok_not\"]\n", 3, "[A-Z][A-Z0-9_]*"},
		{"bad link", head + "link = \"ftp://x\"\n", 3, "http(s) URL"},
		{"unknown profile", head + "notify_profile = \"nope\"\n", 3, `unknown profile "nope"`},
		{"unknown event", head + "[[notify]]\non = [\"boom\"]\nchannels = [\"mail\"]\n", 4, `unknown event "boom"`},
		{"test event", head + "[[notify]]\non = [\"test\"]\nchannels = [\"mail\"]\n", 4, `unknown event "test"`},
		{"unknown channel", head + "[[notify]]\non = [\"fail\"]\nchannels = [\"sms\"]\n", 5, `unknown channel "sms"`},
		{"unknown tag", head + "[[notify]]\non = [\"fail\"]\nchannels = [\"apprise:zzz\"]\n", 5, `tag "zzz"`},
		{"bad remind", head + "[[notify]]\non = [\"fail\"]\nchannels = [\"mail\"]\nremind = \"x\"\n", 6, "invalid duration"},
		{"digest on failure", head + "[[notify]]\non = [\"fail\", \"success\"]\nchannels = [\"mail\"]\ndigest = \"daily 08:00\"\n", 6, "digest only allowed"},
		{"digest on change", head + "[[notify]]\non = [\"success\", \"change\"]\nchannels = [\"mail\"]\ndigest = \"daily 08:00\"\n", 6, "digest only allowed"},
		{"digest on reload", head + "[[notify]]\non = [\"reload\"]\nchannels = [\"mail\"]\ndigest = \"daily 08:00\"\n", 6, "digest only allowed"},
		{"bad digest", head + "[[notify]]\non = [\"success\"]\nchannels = [\"mail\"]\ndigest = \"hourly\"\n", 6, "invalid digest"},
		{"bad weekday", head + "[[notify]]\non = [\"success\"]\nchannels = [\"mail\"]\ndigest = \"weekly Foo 08:00\"\n", 6, "invalid weekday"},
		{"wrong type", head + "timeout = 5\n", 3, "incompatible"},
		{"negative retries", head + "retries = -1\n", 3, "retries"},
		{"bad cron field", "run = \"x.sh\"\nschedule = \"0 6 * *\"\n", 2, "5 fields"},
		{"bad cron step", "run = \"x.sh\"\nschedule = \"*/x * * * *\"\n", 2, "invalid step"},
		{"bad cron range", "run = \"x.sh\"\nschedule = \"5-1 * * * *\"\n", 2, "runs backwards"},
		{"bad cron value", "run = \"x.sh\"\nschedule = \"x * * * *\"\n", 2, "invalid value"},
		{"empty on", head + "[[notify]]\nchannels = [\"mail\"]\n", 3, "at least one event"},
		{"no channels", head + "[[notify]]\non = [\"fail\"]\n", 3, "at least one required"},
		{"negative after", head + "[[notify]]\non = [\"fail\"]\nchannels = [\"mail\"]\nafter = -1\n", 6, "after must not"},
		{"negative tail", head + "[[notify]]\non = [\"fail\"]\nchannels = [\"mail\"]\ntail = -1\n", 6, "tail must not"},
		{"zero remind", head + "[[notify]]\non = [\"fail\"]\nchannels = [\"mail\"]\nremind = \"0s\"\n", 6, "remind must be positive"},
		{"empty window", head + "quiet = \"08:00-08:00\"\n", 3, "is empty"},
		{"negative duration", head + "jitter = \"-5m\"\n", 3, "is negative"},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			dir := write(t, map[string]string{
				"jidoka.toml": okSettings,
				"j/job.toml":  tc.toml,
				"j/x.sh":      "#!/bin/sh\n",
			})

			set := Load(dir, nil)
			if set.SettingsErr != nil {
				t.Fatalf("SettingsErr: %+v", set.SettingsErr)
			}
			if len(set.Jobs) != 0 || len(set.Invalid) == 0 {
				t.Fatalf("want invalid job, got jobs=%d invalid=%+v", len(set.Jobs), set.Invalid)
			}

			e := set.Invalid[0]
			if e.Job != "j" || e.File != "j/job.toml" {
				t.Errorf("identity: %+v", e)
			}
			if e.Line != tc.line {
				t.Errorf("line = %d, want %d (%q)", e.Line, tc.line, e.Msg)
			}
			if !strings.Contains(e.Msg, tc.msg) {
				t.Errorf("msg = %q, want %q", e.Msg, tc.msg)
			}
			checkKey(t, e)
		})
	}
}

func TestWatchErrors(t *testing.T) {
	tests := []jobErr{
		{"neither", "grace = \"1h\"\n", 0, "period or schedule"},
		{"no grace", "period = \"1h\"\n", 0, "grace required"},
		{"both", "period = \"1h\"\nschedule = \"0 * * * *\"\ngrace = \"1h\"\n", 2, "not both"},
		{"bad schedule", "schedule = \"0 99 * * *\"\ngrace = \"1h\"\n", 1, "hour"},
		{"bad period", "period = \"x\"\ngrace = \"1h\"\n", 1, "invalid duration"},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			set := Load(write(t, map[string]string{"w/watch.toml": tc.toml}), nil)
			if len(set.Invalid) == 0 {
				t.Fatalf("want invalid, got %+v", set.Jobs)
			}

			e := set.Invalid[0]
			if e.Line != tc.line || !strings.Contains(e.Msg, tc.msg) || e.File != "w/watch.toml" {
				t.Errorf("got %+v, want line %d %q", e, tc.line, tc.msg)
			}
			checkKey(t, e)
		})
	}
}

func TestBrokenJobKeepsOthers(t *testing.T) {
	dir := write(t, map[string]string{
		"good/job.toml": "run = \"x.sh\"\nschedule = \"0 6 * * *\"\n",
		"good/x.sh":     "",
		"bad/job.toml":  "run = \n",
	})

	set := Load(dir, nil)
	if set.Jobs["good"] == nil || len(set.Jobs) != 1 {
		t.Errorf("jobs: %+v", set.Jobs)
	}
	if len(set.Invalid) != 1 || set.Invalid[0].Job != "bad" {
		t.Errorf("invalid: %+v", set.Invalid)
	}
}

func TestInvalidID(t *testing.T) {
	dir := write(t, map[string]string{
		"Bad_ID/job.toml":  "run = \"x\"\nschedule = \"* * * * *\"\n",
		"Docs/readme.md":   "no job here",
		".hidden/job.toml": "garbage",
	})

	set := Load(dir, nil)
	if len(set.Jobs) != 0 || len(set.Invalid) != 1 {
		t.Fatalf("jobs=%v invalid=%+v", set.Jobs, set.Invalid)
	}
	if e := set.Invalid[0]; e.File != "Bad_ID/job.toml" || !strings.Contains(e.Msg, "[a-z0-9-]+") {
		t.Errorf("invalid: %+v", e)
	}
	checkKey(t, set.Invalid[0])
}

func TestBothFiles(t *testing.T) {
	dir := write(t, map[string]string{
		"a/job.toml":   "run = \"x\"\nschedule = \"* * * * *\"\n",
		"a/watch.toml": "period = \"1h\"\ngrace = \"1h\"\n",
	})

	set := Load(dir, nil)
	if len(set.Invalid) != 1 || !strings.Contains(set.Invalid[0].Msg, "one folder") {
		t.Errorf("invalid: %+v", set.Invalid)
	}
	checkKey(t, set.Invalid[0])
}

func TestSettingsErrors(t *testing.T) {
	tests := []jobErr{
		{"syntax", "zone = \n", 1, ""},
		{"unknown key", "zone = \"UTC\"\ncolour = 1\n", 2, `unknown key "colour"`},
		{"unknown nested key", "[channels.mail]\nsmtp = \"h:25\"\nfrom = \"a@b\"\nbogus = 1\n", 4, "unknown key"},
		{"bad zone", "zone = \"Mars/Base\"\n", 1, "unknown zone"},
		{"bad locale", "locale = \"fr\"\n", 1, "locale must be"},
		{"bad keep", "keep = \"0s\"\n", 1, "positive duration"},
		{"bad quiet", "quiet = \"x\"\n", 1, "invalid window"},
		{"bad parallel", "max_parallel = 0\n", 1, "at least 1"},
		{"bad url", "andon_url = \"nope\"\n", 1, "http(s) URL"},
		{"apprise no url", "[channels.apprise]\ntags = {a = \"b\"}\n", 1, "url required"},
		{"mail no smtp", "[channels.mail]\nfrom = \"a@b\"\n", 1, "smtp required"},
		{"mail no from", "[channels.mail]\nsmtp = \"h:25\"\n", 1, "from required"},
		{"wrong type", "zone = 5\n", 1, "Wrong type"},
		{"profile bad event", "[[profiles.p]]\non = [\"x\"]\nchannels = [\"mail\"]\n", 2, "unknown event"},
		{"profile bad tag", "[[profiles.p]]\non = [\"fail\"]\nchannels = [\"apprise:x\"]\n", 3, "not defined"},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			set := Load(write(t, map[string]string{"jidoka.toml": tc.toml}), nil)
			e := set.SettingsErr
			if e == nil {
				t.Fatal("want SettingsErr")
			}
			if e.File != "jidoka.toml" || e.Job != "" || e.Line != tc.line || !strings.Contains(e.Msg, tc.msg) {
				t.Errorf("got %+v, want line %d %q", e, tc.line, tc.msg)
			}
		})
	}
}

func TestBrokenSettingsKeepPrev(t *testing.T) {
	prev := Load(testdataFull, nil).Settings

	dir := write(t, map[string]string{
		"jidoka.toml": "zone = \n",
		"a/job.toml":  "run = \"x.sh\"\nschedule = \"0 6 * * *\"\nnotify_profile = \"leise\"\n",
		"a/x.sh":      "",
	})

	set := Load(dir, &prev)
	if set.SettingsErr == nil {
		t.Fatal("want SettingsErr")
	}
	if set.Settings.Heartbeat != 5*time.Minute || set.Settings.Mail == nil {
		t.Errorf("prev not kept: %+v", set.Settings)
	}
	// The kept settings still define the profile the job names.
	if set.Jobs["a"] == nil {
		t.Errorf("job lost: %+v", set.Invalid)
	}

	// Without prev: defaults.
	set = Load(dir, nil)
	if set.SettingsErr == nil || set.Settings.MaxParallel != 2 || set.Settings.Zone.String() != "Europe/Berlin" {
		t.Errorf("defaults not used: %+v", set.Settings)
	}
}

func TestHash(t *testing.T) {
	files := map[string]string{
		"a/job.toml": "run = \"x.sh\"\nschedule = \"0 6 * * *\"\n",
		"a/x.sh":     "one",
	}
	dir := write(t, files)
	h1 := Load(dir, nil).Jobs["a"].Hash

	if h2 := Load(dir, nil).Jobs["a"].Hash; h1 != h2 {
		t.Error("hash not stable")
	}

	// Dotfiles do not count.
	if err := os.WriteFile(filepath.Join(dir, "a", ".cache"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if h := Load(dir, nil).Jobs["a"].Hash; h != h1 {
		t.Error("dotfile changed the hash")
	}

	// Content does.
	if err := os.WriteFile(filepath.Join(dir, "a", "x.sh"), []byte("two"), 0o644); err != nil {
		t.Fatal(err)
	}
	if h := Load(dir, nil).Jobs["a"].Hash; h == h1 {
		t.Error("content change not noticed")
	}
}

func TestCommit(t *testing.T) {
	const sha = "0123456789abcdef0123456789abcdef01234567"

	tests := []struct {
		name  string
		files map[string]string
		want  string
	}{
		{"none", map[string]string{"x/y": ""}, ""},
		{"loose ref", map[string]string{
			".git/HEAD":            "ref: refs/heads/main\n",
			".git/refs/heads/main": sha + "\n",
		}, "0123456"},
		{"packed ref", map[string]string{
			".git/HEAD":        "ref: refs/heads/main\n",
			".git/packed-refs": "# pack-refs\n" + sha + " refs/heads/main\n",
		}, "0123456"},
		{"detached", map[string]string{".git/HEAD": sha + "\n"}, "0123456"},
		{"unborn", map[string]string{".git/HEAD": "ref: refs/heads/main\n"}, ""},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := Load(write(t, tc.files), nil).Commit; got != tc.want {
				t.Errorf("Commit = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestParseDur(t *testing.T) {
	tests := map[string]time.Duration{
		"5m": 5 * time.Minute, "26h": 26 * time.Hour, "365d": 365 * 24 * time.Hour, "1h30m": 90 * time.Minute,
	}
	for in, want := range tests {
		if got, err := parseDur(in); err != nil || got != want {
			t.Errorf("parseDur(%q) = %v, %v", in, got, err)
		}
	}
	for _, in := range []string{"", "x", "-1h", "1.5d"} {
		if _, err := parseDur(in); err == nil {
			t.Errorf("parseDur(%q): want error", in)
		}
	}
}

func TestRemindNever(t *testing.T) {
	dir := write(t, map[string]string{
		"a/job.toml": "run = \"x\"\nschedule = \"* * * * *\"\n[[notify]]\non = [\"fail\"]\nchannels = [\"mail\"]\nremind = \"never\"\n",
		"a/x":        "",
	})

	j := Load(dir, nil).Jobs["a"]
	if j == nil || j.Rules[0].Remind != 0 {
		t.Errorf("job: %+v", j)
	}
}

// TestScheduleErrorIsGerman: the cron error nests inside the schedule
// error and both translate.
func TestScheduleErrorIsGerman(t *testing.T) {
	dir := write(t, map[string]string{
		"j/job.toml": "run = \"x.sh\"\nschedule = \"0 25 * * *\"\n",
		"j/x.sh":     "",
	})

	e := Load(dir, nil).Invalid[0]
	got := i18n.T(e.Key, i18n.LocaleDE, e.Params)
	if want := "Ungültiger Zeitplan: Stunde: Wert 25 außerhalb von 0-23"; got != want {
		t.Errorf("German = %q, want %q", got, want)
	}
	if want := "invalid schedule: hour: value 25 out of range 0-23"; e.Msg != want {
		t.Errorf("English = %q, want %q", e.Msg, want)
	}
}

func TestLocaleSetting(t *testing.T) {
	const std = "[[profiles.standard]]\non = [\"fail\"]\nchannels = [\"mail\"]\n"
	tests := map[string]string{"": "de", "locale = \"en\"\n": "en", "locale = \"de\"\n": "de"}
	for src, want := range tests {
		s, err := ParseSettings([]byte(src + std))
		if err != nil || s.Locale != want {
			t.Errorf("%q: locale %q err %v, want %q", src, s.Locale, err, want)
		}
	}
}

// B8: every fallback goes through the standard profile, so a jidoka.toml
// without it is invalid and the last valid copy stays.
func TestSettingsNeedStandardProfile(t *testing.T) {
	const noStandard = "[[profiles.leise]]\non = [\"fail\"]\nchannels = [\"mail\"]\n"

	for name, toml := range map[string]string{"none": "zone = \"UTC\"\n", "other only": noStandard} {
		dir := write(t, map[string]string{"jidoka.toml": toml})
		set := Load(dir, nil)
		if set.SettingsErr == nil || set.SettingsErr.Key != "conf.no_standard" {
			t.Errorf("%s: SettingsErr %+v", name, set.SettingsErr)
			continue
		}
		checkKey(t, *set.SettingsErr)
	}
}

// B8: a missing jidoka.toml is an error like one without a standard
// profile; the last valid copy stays. Local runs may do without it.
func TestMissingSettingsIsError(t *testing.T) {
	dir := write(t, map[string]string{"a/job.toml": "run = \"x.sh\"\nschedule = \"0 6 * * *\"\n", "a/x.sh": ""})

	prev := defaults()
	prev.MaxParallel = 7
	set := Load(dir, &prev)
	if set.SettingsErr == nil || set.SettingsErr.Key != "conf.no_settings" {
		t.Fatalf("SettingsErr %+v", set.SettingsErr)
	}
	checkKey(t, *set.SettingsErr)
	if set.Settings.MaxParallel != 7 || len(set.Jobs) != 1 {
		t.Errorf("settings %+v, jobs %d", set.Settings, len(set.Jobs))
	}

	if local := LoadWith(dir, nil, SettingsOptional); local.SettingsErr != nil {
		t.Errorf("optional: %+v", local.SettingsErr)
	}
}

// An unreadable folder is one error, not also a missing file.
func TestMissingDirIsOneError(t *testing.T) {
	set := Load(filepath.Join(t.TempDir(), "gone"), nil)
	if set.SettingsErr != nil || len(set.Invalid) != 1 {
		t.Errorf("SettingsErr %+v, Invalid %+v", set.SettingsErr, set.Invalid)
	}
}
