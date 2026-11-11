//go:build !release

package demo

import (
	"bytes"
	_ "embed"
	"encoding/json"
	"fmt"
	"math"
	"regexp"
	"strconv"
	"time"
)

// worldJSON is the shared demo world, copied by sync-demo.py from
// shrippen.github.io/demo; never edited here.
//
//go:embed world.json
var worldJSON []byte

// worldKey is the world's section for Jidoka.
const worldKey = "jobs"

// langs of a {"de": …, "en": …} text; en is the fallback.
const (
	langDE = "de"
	langEN = "en"
)

// Kinds of an entry in the world's job list.
const (
	kindWatch = "watch"
)

// data is the world's `jobs` section, texts already in one language.
type data struct {
	DummySecret string         `json:"dummy_secret"`
	Settings    map[string]any `json:"settings"`
	Jobs        []jobDef       `json:"jobs"`
	Secrets     []secretDef    `json:"secrets"`
	Flags       []flagDef      `json:"flags"`
	Alerts      []alertDef     `json:"alerts"`
	Deliveries  []deliveryDef  `json:"deliveries"`
	Audit       []auditDef     `json:"audit"`
}

// jobDef is one folder of the jobs directory plus how its history looks.
type jobDef struct {
	ID      string         `json:"id"`
	Kind    string         `json:"kind"`
	Invalid bool           `json:"invalid"`
	Config  map[string]any `json:"config"` // the keys of job.toml / watch.toml
	Script  scriptDef      `json:"script"`
	History historyDef     `json:"history"`
}

// scriptDef is what the generated script does when it really runs.
type scriptDef struct {
	Sleep   int      `json:"sleep"` // seconds
	Exit    int      `json:"exit"`
	Summary string   `json:"summary"`
	Output  []string `json:"output"`
}

// historyDef describes the 30 days of runs of one job.
type historyDef struct {
	Running       bool         `json:"running"` // the newest slot is running now (catchup at start)
	Until         string       `json:"until"`   // offset: no slot after it (paused, offline)
	Duration      []int        `json:"duration"`
	FailDuration  []int        `json:"fail_duration"`
	PingLag       []int        `json:"ping_lag"` // watches: seconds from slot to ping
	Summaries     []string     `json:"summaries"`
	Output        []string     `json:"output"`
	FailureOutput []string     `json:"failure_output"`
	Changes       []changeDef  `json:"changes"`
	Failures      []failureDef `json:"failures"`
}

// changeDef makes the first slot of a day (from At on) special.
type changeDef struct {
	Day     string   `json:"day"`
	At      string   `json:"at"`
	Summary string   `json:"summary"`
	Message string   `json:"message"`
	Output  []string `json:"output"`
}

// failureDef lets slots of one day, or all since an offset, fail.
type failureDef struct {
	Day     *string `json:"day"`
	Since   string  `json:"since"`
	State   string  `json:"state"`
	Exit    int     `json:"exit"`
	Recover bool    `json:"recover"` // a retry succeeds
}

type secretDef struct {
	Name    string `json:"name"`
	Changed string `json:"changed"`
	By      string `json:"by"`
}

type flagDef struct {
	Job        string `json:"job"`
	Paused     string `json:"paused"`
	By         string `json:"by"`
	Acked      string `json:"acked"`
	MutedUntil string `json:"muted_until"`
}

type alertDef struct {
	Job   string `json:"job"`
	Event string `json:"event"`
	Since string `json:"since"`
}

type deliveryDef struct {
	At      string `json:"at"`
	Job     string `json:"job"`
	Event   string `json:"event"`
	Channel string `json:"channel"`
	OK      bool   `json:"ok"`
	Error   string `json:"error"`
}

type auditDef struct {
	At     string `json:"at"`
	Actor  string `json:"actor"`
	Action string `json:"action"`
	Target string `json:"target"`
}

// loadData reads the `jobs` section in one language.
func loadData(lang string) (data, error) {
	var d data

	dec := json.NewDecoder(bytes.NewReader(worldJSON))
	dec.UseNumber()

	var all map[string]any
	if err := dec.Decode(&all); err != nil {
		return d, fmt.Errorf("world: %w", err)
	}

	section, ok := all[worldKey]
	if !ok {
		return d, fmt.Errorf("world: no %q section", worldKey)
	}

	raw, err := json.Marshal(localize(section, lang))
	if err != nil {
		return d, err
	}
	if err := json.Unmarshal(raw, &d); err != nil {
		return d, fmt.Errorf("world: %w", err)
	}

	return d, nil
}

// localize picks one language from every {"de": …, "en": …} text.
func localize(v any, lang string) any {
	switch x := v.(type) {
	case map[string]any:
		if s, ok := pickLang(x, lang); ok {
			return s
		}
		for k, e := range x {
			x[k] = localize(e, lang)
		}
		return x
	case []any:
		for i, e := range x {
			x[i] = localize(e, lang)
		}
		return x
	}
	return v
}

// pickLang reads a map that holds only language keys with text values.
func pickLang(m map[string]any, lang string) (string, bool) {
	if len(m) == 0 {
		return "", false
	}
	for k, v := range m {
		if _, isText := v.(string); !isText || (k != langDE && k != langEN) {
			return "", false
		}
	}

	if s, ok := m[lang].(string); ok {
		return s, true
	}
	s, _ := m[langEN].(string)
	return s, true
}

// tomlValue turns decoded JSON into what the TOML encoder writes as
// intended: whole numbers as integers.
func tomlValue(v any) any {
	switch x := v.(type) {
	case map[string]any:
		out := make(map[string]any, len(x))
		for k, e := range x {
			out[k] = tomlValue(e)
		}
		return out
	case []any:
		out := make([]map[string]any, 0, len(x))
		plain := make([]any, 0, len(x))
		for _, e := range x {
			m, isMap := tomlValue(e).(map[string]any)
			if isMap {
				out = append(out, m)
				continue
			}
			plain = append(plain, tomlValue(e))
		}
		if len(out) > 0 {
			return out
		}
		return plain
	case float64:
		if x == math.Trunc(x) {
			return int64(x)
		}
	}
	return v
}

var offsetRe = regexp.MustCompile(`^@([+-])(?:(\d+)d)?(?:(\d+)h)?(?:(\d+)m)?$`)

// offset resolves "@-3h", "@+22h", "@-1d6h" against now.
func offset(s string, now time.Time) (time.Time, error) {
	m := offsetRe.FindStringSubmatch(s)
	if m == nil {
		return time.Time{}, fmt.Errorf("demo: bad time offset %q", s)
	}

	var d time.Duration
	for i, unit := range []time.Duration{24 * time.Hour, time.Hour, time.Minute} {
		if m[i+2] == "" {
			continue
		}
		n, _ := strconv.Atoi(m[i+2])
		d += time.Duration(n) * unit
	}
	if m[1] == "-" {
		d = -d
	}

	return now.Add(d), nil
}

var dayRe = regexp.MustCompile(`^@date([+-]\d+)$`)

// dayOffset reads "@date-2" as -2 (days from today).
func dayOffset(s string) (int, error) {
	m := dayRe.FindStringSubmatch(s)
	if m == nil {
		return 0, fmt.Errorf("demo: bad day %q", s)
	}

	return strconv.Atoi(m[1])
}
