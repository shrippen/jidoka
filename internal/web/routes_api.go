package web

import (
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"

	"jidoka/internal/model"
	"jidoka/internal/services/access"
	"jidoka/internal/services/plan"
	"jidoka/internal/services/status"
)

// The JSON API (I1, Z2). Times are RFC 3339 in UTC, absent values are null,
// never a secret: run texts were masked when they were stored.
//
//	GET  /api/v1/status              read
//	GET  /api/v1/jobs                read    ?limit
//	GET  /api/v1/jobs/{id}/runs      read    ?limit&before
//	GET  /api/v1/runs/{id}           read    with output
//	POST /api/v1/reload              reload
//	POST /api/v1/jobs/{id}/run       run
const (
	apiBase      = "/api/v1"
	apiActor     = "api"
	paramLimit   = "limit"
	paramBefore  = "before"
	errKeyNone   = "error.not_found"
	errKeyBad    = "error.bad_request"
	errKeyBusy   = "error.busy"
	errKeyOff    = "error.disabled"
	errKeyNoSvc  = "error.unavailable"
	jobPageBase  = "/jobs/"
	contentJSON  = "application/json"
	noStoreCache = "no-store"
)

func (s *Server) registerAPI() {
	read := func(h http.HandlerFunc) http.HandlerFunc { return s.api(access.RightRead, h) }

	s.mux.HandleFunc("GET "+apiBase+"/status", read(s.apiStatus))
	s.mux.HandleFunc("GET "+apiBase+"/jobs", read(s.apiJobs))
	s.mux.HandleFunc("GET "+apiBase+"/jobs/{id}/runs", read(s.apiJobRuns))
	s.mux.HandleFunc("GET "+apiBase+"/runs/{id}", read(s.apiRun))
	s.mux.HandleFunc("POST "+apiBase+"/reload", s.api(access.RightReload, s.apiReload))
	s.mux.HandleFunc("POST "+apiBase+"/jobs/{id}/run", s.api(access.RightRun, s.apiRunJob))
}

// apiTime formats t as RFC 3339 UTC; nil for the zero time.
func apiTime(t time.Time) *string {
	if t.IsZero() {
		return nil
	}
	v := t.UTC().Format(time.RFC3339)
	return &v
}

func apiText(v string) *string {
	if v == "" {
		return nil
	}
	return &v
}

func (s *Server) writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", contentJSON)
	w.Header().Set("Cache-Control", noStoreCache)
	w.WriteHeader(code)
	if err := json.NewEncoder(w).Encode(v); err != nil {
		s.log.Error("api encode", "err", err)
	}
}

func (s *Server) apiFail(w http.ResponseWriter, code int, key string) {
	s.apiError(w, code, errors.New(key))
}

// apiInternal logs err and answers 500.
func (s *Server) apiInternal(w http.ResponseWriter, what string, err error) {
	s.log.Error(what, "err", err)
	s.apiFail(w, http.StatusInternalServerError, errKeyInternal)
}

// pageOf reads limit and before; false when one is not a number.
func pageOf(r *http.Request) (status.Page, bool) {
	var pg status.Page
	q := r.URL.Query()

	if v := q.Get(paramLimit); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n < 0 {
			return pg, false
		}
		pg.Limit = n
	}
	if v := q.Get(paramBefore); v != "" {
		n, err := strconv.ParseInt(v, 10, 64)
		if err != nil || n < 0 {
			return pg, false
		}
		pg.Before = n
	}
	return pg, true
}

type apiChannel struct {
	Channel string  `json:"channel"`
	Down    bool    `json:"down"`
	Since   *string `json:"since"`
	LastOK  *string `json:"last_ok"`
}

type apiStatus struct {
	Version    string       `json:"version"`
	Commit     string       `json:"commit"`
	ReloadedAt *string      `json:"reloaded_at"`
	Invalid    int          `json:"invalid"`
	Channels   []apiChannel `json:"channels"`
	LastTick   *string      `json:"last_tick"`
}

func (s *Server) apiStatus(w http.ResponseWriter, r *http.Request) {
	if s.deps.Status == nil {
		s.apiFail(w, http.StatusServiceUnavailable, errKeyNoSvc)
		return
	}

	s.writeJSON(w, http.StatusOK, s.statusOf())
}

// statusOf is /api/v1/status; /api/v1/jobs carries it too, so Andon
// needs one request per fetch.
func (s *Server) statusOf() apiStatus {
	st := s.deps.Status.Status()
	out := apiStatus{
		Version: st.Version, Commit: st.Commit, ReloadedAt: apiTime(st.ReloadedAt),
		Invalid: st.Invalid, LastTick: apiTime(st.LastTick), Channels: []apiChannel{},
	}
	// The error text stays out: Andon needs to know that a channel is down.
	for _, c := range st.Channels {
		out.Channels = append(out.Channels, apiChannel{
			Channel: c.Channel, Down: c.Down, LastOK: apiTime(c.LastOK),
			Since: apiTime(c.Since),
		})
	}
	return out
}

type apiRunRef struct {
	ID    int64          `json:"id"`
	State model.RunState `json:"state"`
	Start *string        `json:"start"`
	End   *string        `json:"end"`
}

type apiJob struct {
	ID      string         `json:"id"`
	Name    string         `json:"name"`
	Kind    model.Kind     `json:"kind"`
	State   model.JobState `json:"state"`
	Reason  *string        `json:"reason"`
	Summary string         `json:"summary"`
	Since   *string        `json:"since"`
	Streak  int            `json:"streak"`
	Last    *apiRunRef     `json:"last"`
	Next    *string        `json:"next"`
	// Tail is the last 20 lines of the newest run's output.
	Tail string `json:"tail"`
	URL  string `json:"url"`
	// History holds the last runs, oldest first, for Andon's popup (I5).
	History  []apiRunRef `json:"history"`
	Tags     []string    `json:"tags"`
	Schedule string      `json:"schedule"`
}

// historyMax is how many runs a job's history in /api/v1/jobs holds.
const historyMax = 16

func (s *Server) jobURL(id string) string {
	return strings.TrimRight(s.settings().BaseURL, "/") + jobPageBase + id
}

func (s *Server) apiJobs(w http.ResponseWriter, r *http.Request) {
	pg, ok := pageOf(r)
	if !ok {
		s.apiFail(w, http.StatusBadRequest, errKeyBad)
		return
	}
	if s.deps.Status == nil {
		s.apiFail(w, http.StatusServiceUnavailable, errKeyNoSvc)
		return
	}

	b, err := s.deps.Status.Overview()
	if err != nil {
		s.apiInternal(w, "api jobs", err)
		return
	}

	tiles := b.Tiles
	if limit := apiLimit(pg.Limit); len(tiles) > limit {
		tiles = tiles[:limit]
	}

	out := make([]apiJob, 0, len(tiles))
	for _, t := range tiles {
		j := apiJob{
			ID: t.Job.ID, Name: t.Job.Name, Kind: t.Job.Kind, State: t.State,
			Reason: apiText(string(t.Reason)), Summary: t.Summary, Since: apiTime(t.Since),
			Streak: t.Streak, Next: apiTime(t.Next), Tail: t.Tail, URL: s.jobURL(t.Job.ID),
			Tags: t.Job.Tags, Schedule: t.Job.Schedule, History: historyOf(t.Ticks),
		}
		if t.Last != nil {
			j.Last = &apiRunRef{ID: t.Last.ID, State: t.Last.State, Start: apiTime(t.Last.Start), End: apiTime(t.Last.End)}
		}
		out = append(out, j)
	}
	s.writeJSON(w, http.StatusOK, map[string]any{"jobs": out, "total": len(b.Tiles), "status": s.statusOf()})
}

// historyOf keeps the newest historyMax ticks, oldest first.
func historyOf(ticks []status.Tick) []apiRunRef {
	if len(ticks) > historyMax {
		ticks = ticks[len(ticks)-historyMax:]
	}

	out := make([]apiRunRef, 0, len(ticks))
	for _, t := range ticks {
		out = append(out, apiRunRef{ID: t.ID, State: t.State, Start: apiTime(t.Start), End: apiTime(t.End)})
	}
	return out
}

// apiLimit applies the default and the maximum of a list (I1).
func apiLimit(n int) int {
	switch {
	case n <= 0:
		return status.PageDefault
	case n > status.PageMax:
		return status.PageMax
	}
	return n
}

type apiRun struct {
	ID         int64          `json:"id"`
	Job        string         `json:"job"`
	Trigger    model.Trigger  `json:"trigger"`
	State      model.RunState `json:"state"`
	Slot       *string        `json:"slot"`
	Attempt    int            `json:"attempt"`
	Start      *string        `json:"start"`
	End        *string        `json:"end"`
	DurationMS int64          `json:"duration_ms"`
	Exit       *int           `json:"exit"`
	Summary    string         `json:"summary"`
	Message    string         `json:"message,omitempty"`
	Error      string         `json:"error,omitempty"`
	Commit     string         `json:"commit"`
	Dry        bool           `json:"dry"`
	URL        string         `json:"url"`
	Output     *string        `json:"output,omitempty"`
}

func (s *Server) runJSON(r model.Run, now time.Time) apiRun {
	return apiRun{
		ID: r.ID, Job: r.Job, Trigger: r.Trigger, State: r.State, Slot: apiTime(r.Slot),
		Attempt: r.Attempt, Start: apiTime(r.Started), End: apiTime(r.Ended),
		DurationMS: r.Duration(now).Milliseconds(), Exit: r.Exit, Summary: r.Summary,
		Message: r.Message, Error: r.Error, Commit: r.Commit, Dry: r.Dry,
		URL: strings.TrimRight(s.settings().BaseURL, "/") + "/runs/" + strconv.FormatInt(r.ID, 10),
	}
}

func (s *Server) apiJobRuns(w http.ResponseWriter, r *http.Request) {
	pg, ok := pageOf(r)
	if !ok {
		s.apiFail(w, http.StatusBadRequest, errKeyBad)
		return
	}
	if s.deps.Status == nil {
		s.apiFail(w, http.StatusServiceUnavailable, errKeyNoSvc)
		return
	}

	runs, next, err := s.deps.Status.Runs(r.PathValue("id"), pg)
	if errors.Is(err, status.ErrNotFound) {
		s.apiFail(w, http.StatusNotFound, errKeyNone)
		return
	}
	if err != nil {
		s.apiInternal(w, "api runs", err)
		return
	}

	now := time.Now()
	out := make([]apiRun, 0, len(runs))
	for _, run := range runs {
		out = append(out, s.runJSON(run, now))
	}

	var nextBefore *int64
	if next != 0 {
		nextBefore = &next
	}
	s.writeJSON(w, http.StatusOK, map[string]any{"runs": out, "next_before": nextBefore})
}

func (s *Server) apiRun(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		s.apiFail(w, http.StatusNotFound, errKeyNone)
		return
	}
	if s.deps.Status == nil {
		s.apiFail(w, http.StatusServiceUnavailable, errKeyNoSvc)
		return
	}

	p, err := s.deps.Status.Run(id)
	if errors.Is(err, status.ErrNotFound) {
		s.apiFail(w, http.StatusNotFound, errKeyNone)
		return
	}
	if err != nil {
		s.apiInternal(w, "api run", err)
		return
	}

	out := s.runJSON(p.Run, time.Now())
	out.Output = &p.Run.Output
	s.writeJSON(w, http.StatusOK, out)
}

type apiDiff struct {
	Added   []string `json:"added"`
	Changed []string `json:"changed"`
	Removed []string `json:"removed"`
	Invalid []string `json:"invalid"`
	Commit  string   `json:"commit"`
	At      *string  `json:"at"`
}

// nonNil keeps an empty list as [] in JSON.
func nonNil(l []string) []string {
	if l == nil {
		return []string{}
	}
	return l
}

func (s *Server) apiReload(w http.ResponseWriter, r *http.Request) {
	if s.deps.Ops == nil {
		s.apiFail(w, http.StatusServiceUnavailable, errKeyNoSvc)
		return
	}

	d, err := s.deps.Ops.Reload(apiActor)
	if err != nil {
		s.apiInternal(w, "api reload", err)
		return
	}

	s.writeJSON(w, http.StatusOK, apiDiff{
		Added: nonNil(d.Added), Changed: nonNil(d.Changed), Removed: nonNil(d.Removed),
		Invalid: nonNil(d.Invalid), Commit: d.Commit, At: apiTime(d.At),
	})
}

func (s *Server) apiRunJob(w http.ResponseWriter, r *http.Request) {
	if s.deps.Plan == nil {
		s.apiFail(w, http.StatusServiceUnavailable, errKeyNoSvc)
		return
	}

	id := r.PathValue("id")
	err := s.deps.Plan.RunNow(id, apiActor)
	switch {
	case errors.Is(err, plan.ErrUnknownJob):
		s.apiFail(w, http.StatusNotFound, errKeyNone)
	case errors.Is(err, plan.ErrBusy):
		s.apiFail(w, http.StatusConflict, errKeyBusy)
	case errors.Is(err, plan.ErrDisabled):
		s.apiFail(w, http.StatusConflict, errKeyOff)
	case errors.Is(err, plan.ErrStopped):
		s.apiFail(w, http.StatusServiceUnavailable, errKeyNoSvc)
	case err != nil:
		s.apiInternal(w, "api run job", err)
	default:
		s.writeJSON(w, http.StatusAccepted, map[string]string{"job": id})
	}
}
