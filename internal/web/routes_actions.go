package web

import (
	"errors"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"jidoka/internal/crypto"
	"jidoka/internal/services/ops"
	"jidoka/internal/services/plan"
	"jidoka/internal/services/secrets"
	"jidoka/internal/services/watch"
)

// UI actions: POST only, session and CSRF through s.authed (Z4). Each
// answers 303 to the form's next field (a local path) or to a default, and
// for htmx an HX-Redirect with the same target. A change writes an audit
// entry with the signed-in name; the secret value is never echoed or logged.
//
//	POST /reload
//	POST /jobs/{id}/run | dry | pause | resume | ack | mute | unmute | renew-token
//	POST /jobs/{id}/rules/{idx}/test          test message of one rule (M11)
//	POST /runs/{id}/cancel
//	POST /channels/{channel}/test             test message of one channel (M11)
//	POST /secrets                             fields name, value
//	POST /secrets/{name}/delete
const (
	fieldNext   = "next"
	fieldSpan   = "span"
	fieldValue  = "value"
	fieldSecret = "name"

	errKeyNotRunning = "error.not_running"
	errKeyNoKey      = "error.no_key"
	errKeySecretName = "error.secret_name"
	errKeySecretVal  = "error.secret_empty"

	hxRedirect = "HX-Redirect"
	hxRequest  = "HX-Request"

	secretsPath  = "/secrets"
	channelsPath = "/channels"
	runsPath     = "/runs/"
)

func (s *Server) registerActions() {
	post := func(path string, h func(http.ResponseWriter, *http.Request, *session)) {
		s.mux.HandleFunc("POST "+path, s.authed(h))
	}

	post("/reload", s.actReload)
	post("/jobs/{id}/run", s.jobAction(s.actRun))
	post("/jobs/{id}/dry", s.jobAction(s.actDry))
	post("/jobs/{id}/pause", s.jobAction(s.actPause))
	post("/jobs/{id}/resume", s.jobAction(s.actResume))
	post("/jobs/{id}/ack", s.jobAction(s.actAck))
	post("/jobs/{id}/mute", s.actMute)
	post("/jobs/{id}/unmute", s.jobAction(s.actUnmute))
	post("/jobs/{id}/renew-token", s.jobAction(s.actRenew))
	post("/jobs/{id}/rules/{idx}/test", s.actTestRule)
	post("/runs/{id}/cancel", s.actCancel)
	post("/channels/{channel}/test", s.actTestChannel)
	post(secretsPath, s.actSecretSet)
	post(secretsPath+"/{name}/delete", s.actSecretDelete)
}

// actor is the name written to the audit log.
func actor(sess *session) string {
	if sess.Who.Name != "" {
		return sess.Who.Name
	}
	return sess.Who.Sub
}

// back leaves the action: to the form's next field if it is a local path,
// else to fallback.
func (s *Server) back(w http.ResponseWriter, r *http.Request, fallback string) {
	target := fallback
	if next := r.PostForm.Get(fieldNext); next != "" {
		target = safeNext(next)
	}

	// htmx would swap a redirected page into the fragment; it wants a full load.
	if r.Header.Get(hxRequest) != "" {
		w.Header().Set(hxRedirect, target)
		w.WriteHeader(http.StatusOK)
		return
	}
	http.Redirect(w, r, target, http.StatusSeeOther)
}

// actionFail answers the error of an action with a page.
func (s *Server) actionFail(w http.ResponseWriter, r *http.Request, sess *session, err error) {
	code, key := http.StatusInternalServerError, errKeyInternal

	switch {
	case errors.Is(err, plan.ErrUnknownJob), errors.Is(err, ops.ErrUnknownJob),
		errors.Is(err, watch.ErrUnknown), errors.Is(err, secrets.ErrNotFound):
		code, key = http.StatusNotFound, errKeyNone
	case errors.Is(err, plan.ErrStopped):
		code, key = http.StatusServiceUnavailable, errKeyNoSvc
	case errors.Is(err, plan.ErrBusy):
		code, key = http.StatusConflict, errKeyBusy
	case errors.Is(err, plan.ErrDisabled):
		code, key = http.StatusConflict, errKeyOff
	case errors.Is(err, ops.ErrNotRunning):
		code, key = http.StatusConflict, errKeyNotRunning
	case errors.Is(err, ops.ErrBadSpan):
		code, key = http.StatusBadRequest, errKeyBad
	case errors.Is(err, secrets.ErrBadName):
		code, key = http.StatusBadRequest, errKeySecretName
	case errors.Is(err, secrets.ErrEmpty):
		code, key = http.StatusBadRequest, errKeySecretVal
	case errors.Is(err, crypto.ErrMissingKey):
		code, key = http.StatusConflict, errKeyNoKey
	}

	if code == http.StatusInternalServerError {
		s.log.Error("action", "route", routeOf(r), "err", err)
	}
	s.fail(w, r, sess, code, key)
}

// jobAction wraps an action on {id} that needs only the job id and the
// actor: run it, then go back to the job page.
func (s *Server) jobAction(do func(id, who string) error) func(http.ResponseWriter, *http.Request, *session) {
	return func(w http.ResponseWriter, r *http.Request, sess *session) {
		if err := r.ParseForm(); err != nil {
			s.fail(w, r, sess, http.StatusBadRequest, errKeyBad)
			return
		}

		id := r.PathValue("id")
		if err := do(id, actor(sess)); err != nil {
			s.actionFail(w, r, sess, err)
			return
		}
		s.back(w, r, jobPageBase+url.PathEscape(id))
	}
}

// missing answers an action whose service is not wired (tests, startup).
func (s *Server) missing(w http.ResponseWriter, r *http.Request, sess *session) {
	s.fail(w, r, sess, http.StatusServiceUnavailable, errKeyNoSvc)
}

func (s *Server) actRun(id, who string) error {
	if s.deps.Plan == nil {
		return plan.ErrStopped
	}
	return s.deps.Plan.RunNow(id, who)
}

func (s *Server) actDry(id, who string) error {
	if s.deps.Plan == nil {
		return plan.ErrStopped
	}
	return s.deps.Plan.DryRun(id, who)
}

func (s *Server) actPause(id, who string) error {
	if s.deps.Plan == nil {
		return plan.ErrStopped
	}
	return s.deps.Plan.Pause(id, who)
}

func (s *Server) actResume(id, who string) error {
	if s.deps.Plan == nil {
		return plan.ErrStopped
	}
	return s.deps.Plan.Resume(id, who)
}

func (s *Server) actAck(id, who string) error {
	if s.deps.Ops == nil {
		return plan.ErrStopped
	}
	return s.deps.Ops.Ack(id, who)
}

func (s *Server) actUnmute(id, who string) error {
	if s.deps.Ops == nil {
		return plan.ErrStopped
	}
	return s.deps.Ops.Unmute(id, who)
}

func (s *Server) actRenew(id, who string) error {
	if s.deps.Ops == nil {
		return plan.ErrStopped
	}
	return s.deps.Ops.Renew(id, who)
}

// actMute reads the span (1h, 1d, recover) from the form.
func (s *Server) actMute(w http.ResponseWriter, r *http.Request, sess *session) {
	if s.deps.Ops == nil {
		s.missing(w, r, sess)
		return
	}
	if err := r.ParseForm(); err != nil {
		s.fail(w, r, sess, http.StatusBadRequest, errKeyBad)
		return
	}

	span, ok := ops.ParseSpan(r.PostForm.Get(fieldSpan))
	if !ok {
		s.fail(w, r, sess, http.StatusBadRequest, errKeyBad)
		return
	}

	id := r.PathValue("id")
	if err := s.deps.Ops.Mute(id, span, actor(sess)); err != nil {
		s.actionFail(w, r, sess, err)
		return
	}
	s.back(w, r, jobPageBase+url.PathEscape(id))
}

func (s *Server) actReload(w http.ResponseWriter, r *http.Request, sess *session) {
	if s.deps.Ops == nil {
		s.missing(w, r, sess)
		return
	}
	if err := r.ParseForm(); err != nil {
		s.fail(w, r, sess, http.StatusBadRequest, errKeyBad)
		return
	}

	if _, err := s.deps.Ops.Reload(actor(sess)); err != nil {
		s.actionFail(w, r, sess, err)
		return
	}
	s.back(w, r, homePath)
}

func (s *Server) actCancel(w http.ResponseWriter, r *http.Request, sess *session) {
	if s.deps.Ops == nil {
		s.missing(w, r, sess)
		return
	}
	if err := r.ParseForm(); err != nil {
		s.fail(w, r, sess, http.StatusBadRequest, errKeyBad)
		return
	}

	raw := r.PathValue("id")
	id, err := strconv.ParseInt(raw, 10, 64)
	if err != nil {
		s.fail(w, r, sess, http.StatusNotFound, errKeyNone)
		return
	}

	if err := s.deps.Ops.Cancel(id, actor(sess)); err != nil {
		s.actionFail(w, r, sess, err)
		return
	}
	s.back(w, r, runsPath+raw)
}

// actTestChannel sends a test message. The result is a delivery, which the
// channels page lists with its error, so a failed send still goes back.
func (s *Server) actTestChannel(w http.ResponseWriter, r *http.Request, sess *session) {
	if s.deps.Alerts == nil {
		s.missing(w, r, sess)
		return
	}
	if err := r.ParseForm(); err != nil {
		s.fail(w, r, sess, http.StatusBadRequest, errKeyBad)
		return
	}

	channel := r.PathValue("channel")
	if err := s.deps.Alerts.TestChannel(r.Context(), channel); err != nil {
		s.log.Warn("test message", "channel", channel, "err", err)
	}
	s.back(w, r, channelsPath)
}

func (s *Server) actTestRule(w http.ResponseWriter, r *http.Request, sess *session) {
	if s.deps.Alerts == nil {
		s.missing(w, r, sess)
		return
	}
	if err := r.ParseForm(); err != nil {
		s.fail(w, r, sess, http.StatusBadRequest, errKeyBad)
		return
	}

	idx, err := strconv.Atoi(r.PathValue("idx"))
	if err != nil || idx < 0 {
		s.fail(w, r, sess, http.StatusNotFound, errKeyNone)
		return
	}

	id := r.PathValue("id")
	if err := s.deps.Alerts.TestRule(r.Context(), id, idx); err != nil {
		s.log.Warn("test message", "job", id, "rule", idx, "err", err)
	}
	s.back(w, r, jobPageBase+url.PathEscape(id))
}

// actSecretSet stores or replaces a secret. An error page names no value.
func (s *Server) actSecretSet(w http.ResponseWriter, r *http.Request, sess *session) {
	if s.deps.Secrets == nil {
		s.missing(w, r, sess)
		return
	}
	if err := r.ParseForm(); err != nil {
		s.fail(w, r, sess, http.StatusBadRequest, errKeyBad)
		return
	}

	name := strings.TrimSpace(r.PostForm.Get(fieldSecret))
	if err := s.deps.Secrets.Set(name, r.PostForm.Get(fieldValue), actor(sess)); err != nil {
		s.actionFail(w, r, sess, err)
		return
	}
	s.back(w, r, secretsPath)
}

func (s *Server) actSecretDelete(w http.ResponseWriter, r *http.Request, sess *session) {
	if s.deps.Secrets == nil {
		s.missing(w, r, sess)
		return
	}
	if err := r.ParseForm(); err != nil {
		s.fail(w, r, sess, http.StatusBadRequest, errKeyBad)
		return
	}

	if err := s.deps.Secrets.Delete(r.PathValue("name"), actor(sess)); err != nil {
		s.actionFail(w, r, sess, err)
		return
	}
	s.back(w, r, secretsPath)
}
