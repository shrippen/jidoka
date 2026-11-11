package web

import (
	"errors"
	"net/http"
	"strconv"
	"time"

	"jidoka/internal/model"
	"jidoka/internal/services/status"
)

// The pages of Jidoka (O1 to O4, O8, O11): GET only, behind the login.
// They read through the status service and render with templates/<page>.html.
//
//	GET /                 board (O1)        GET /board/tiles   its poll fragment
//	GET /plan             timetable (O2)    ?day=-1|0|1
//	GET /jobs/{id}        job (O3)          ?before=<run id>
//	GET /runs/{id}        run (O4)
//	GET /channels         channels (O8)
//	GET /secrets          secrets (O11)     ?name=<prefill>
const (
	boardTilesPath = "/board/tiles"
	planPath       = "/plan"
	paramDay       = "day"
	paramName      = "name"
	dayBack        = -1
	dayAhead       = 1
)

func (s *Server) registerPages() {
	get := func(path string, h func(http.ResponseWriter, *http.Request, *session)) {
		s.mux.HandleFunc("GET "+path, s.authed(h))
	}

	get("/{$}", s.pageBoard)
	get(boardTilesPath, s.fragBoard)
	get(planPath, s.pagePlan)
	get("/jobs/{id}", s.pageJob)
	get("/runs/{id}", s.pageRun)
	get(channelsPath, s.pageChannels)
	get(secretsPath, s.pageSecrets)
}

// reader is the status service, or nil after answering "unavailable".
func (s *Server) reader(w http.ResponseWriter, r *http.Request, sess *session) *status.Service {
	if s.deps.Status == nil {
		s.fail(w, r, sess, http.StatusServiceUnavailable, errKeyNoSvc)
		return nil
	}
	return s.deps.Status
}

// readFail answers a failed read: not found, or an internal error.
func (s *Server) readFail(w http.ResponseWriter, r *http.Request, sess *session, err error) {
	if errors.Is(err, status.ErrNotFound) {
		s.fail(w, r, sess, http.StatusNotFound, errKeyNone)
		return
	}

	s.log.Error("page", "route", routeOf(r), "err", err)
	s.fail(w, r, sess, http.StatusInternalServerError, errKeyInternal)
}

// ── board ──

// boardCounts sums the tiles for the line over the board.
type boardCounts struct {
	Bad, Missed, Running, OK, Idle int
}

// boardPage is the data of board.html.
type boardPage struct {
	Board   status.Board
	Counts  boardCounts
	Removed []status.Removed
}

func countsOf(c status.Counts) boardCounts {
	by := c.ByState
	return boardCounts{
		Bad:     by[model.JobFail] + by[model.JobTimeout] + by[model.JobInvalid],
		Missed:  by[model.JobMissed],
		Running: by[model.JobRunning],
		OK:      by[model.JobOK],
		Idle:    by[model.JobPaused] + by[model.JobDisabled] + by[model.JobNever],
	}
}

// boardOf reads the board; nil after an error page. Without a status
// service (tests, startup) the board is empty: the frame still shows.
func (s *Server) boardOf(w http.ResponseWriter, r *http.Request, sess *session) *boardPage {
	if s.deps.Status == nil {
		return &boardPage{Board: status.Board{Tiles: []status.Tile{}, Invalid: []model.LoadError{}}}
	}

	st := s.deps.Status

	b, err := st.Board()
	if err != nil {
		s.readFail(w, r, sess, err)
		return nil
	}

	gone, err := st.Removed()
	if err != nil {
		s.readFail(w, r, sess, err)
		return nil
	}
	return &boardPage{Board: b, Counts: countsOf(b.Counts), Removed: gone}
}

func (s *Server) pageBoard(w http.ResponseWriter, r *http.Request, sess *session) {
	if p := s.boardOf(w, r, sess); p != nil {
		s.page(w, r, sess, "board", http.StatusOK, p)
	}
}

// fragBoard is the board without its frame: what the 15 s poll swaps in.
func (s *Server) fragBoard(w http.ResponseWriter, r *http.Request, sess *session) {
	if p := s.boardOf(w, r, sess); p != nil {
		s.fragment(w, r, sess, "board", "boardwrap", p)
	}
}

// handleNotFound answers every path no route took. Behind the login, so a
// stranger learns nothing about which paths exist.
func (s *Server) handleNotFound(w http.ResponseWriter, r *http.Request, sess *session) {
	s.fail(w, r, sess, http.StatusNotFound, "error.not_found")
}

// ── timetable ──

// planDay is one button of the day switch.
type planDay struct {
	Value int
	Label string
	On    bool
}

// planPage is the data of plan.html.
type planPage struct {
	TT   status.Timetable
	Days []planDay
}

func (s *Server) pagePlan(w http.ResponseWriter, r *http.Request, sess *session) {
	st := s.reader(w, r, sess)
	if st == nil {
		return
	}

	day := 0
	if n, err := strconv.Atoi(r.URL.Query().Get(paramDay)); err == nil && n >= dayBack && n <= dayAhead {
		day = n
	}

	u := s.uiOf(r)
	tt, err := st.Timetable(u.now.In(u.zone).AddDate(0, 0, day))
	if err != nil {
		s.readFail(w, r, sess, err)
		return
	}

	p := planPage{TT: tt}
	for _, d := range []struct {
		v   int
		key string
	}{{dayBack, "plan.yesterday"}, {0, "plan.today"}, {dayAhead, "plan.tomorrow"}} {
		p.Days = append(p.Days, planDay{Value: d.v, Label: u.T(d.key), On: d.v == day})
	}
	s.page(w, r, sess, "plan", http.StatusOK, p)
}

// ── job and run ──

// jobPage is the data of job.html.
type jobPage struct {
	status.JobPage
	// Paged: the list does not start at the newest run.
	Paged bool
}

func (s *Server) pageJob(w http.ResponseWriter, r *http.Request, sess *session) {
	st := s.reader(w, r, sess)
	if st == nil {
		return
	}

	before, _ := strconv.ParseInt(r.URL.Query().Get(paramBefore), 10, 64)
	if before < 0 {
		before = 0
	}

	p, err := st.Job(r.PathValue("id"), status.Page{Before: before})
	if err != nil {
		s.readFail(w, r, sess, err)
		return
	}
	s.page(w, r, sess, "job", http.StatusOK, jobPage{JobPage: p, Paged: before > 0})
}

func (s *Server) pageRun(w http.ResponseWriter, r *http.Request, sess *session) {
	st := s.reader(w, r, sess)
	if st == nil {
		return
	}

	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		s.fail(w, r, sess, http.StatusNotFound, errKeyNone)
		return
	}

	p, err := st.Run(id)
	if err != nil {
		s.readFail(w, r, sess, err)
		return
	}
	s.page(w, r, sess, "run", http.StatusOK, p)
}

// ── channels and secrets ──

func (s *Server) pageChannels(w http.ResponseWriter, r *http.Request, sess *session) {
	st := s.reader(w, r, sess)
	if st == nil {
		return
	}

	p, err := st.Channels()
	if err != nil {
		s.readFail(w, r, sess, err)
		return
	}
	s.page(w, r, sess, "channels", http.StatusOK, p)
}

// secretsPage is the data of secrets.html. Prefill is the name a link
// asked to set; it never carries a value.
type secretsPage struct {
	status.SecretsPage
	Prefill string
}

func (s *Server) pageSecrets(w http.ResponseWriter, r *http.Request, sess *session) {
	st := s.reader(w, r, sess)
	if st == nil {
		return
	}

	p, err := st.Secrets()
	if err != nil {
		s.readFail(w, r, sess, err)
		return
	}
	s.page(w, r, sess, "secrets", http.StatusOK, secretsPage{SecretsPage: p, Prefill: r.URL.Query().Get(paramName)})
}

// quietClock writes a time of day since midnight as "22:00".
func quietClock(d time.Duration) string {
	return time.Time{}.Add(d).Format("15:04")
}
