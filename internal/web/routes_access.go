package web

import (
	"net/http"
	"strconv"

	"jidoka/internal/services/access"
)

const (
	accessPath   = "/access"
	fieldName    = "name"
	fieldRight   = "right"
	maxTokenName = 80
)

func (s *Server) registerAccess() {
	s.mux.HandleFunc("GET "+accessPath, s.authed(s.handleAccess))
	s.mux.HandleFunc("POST "+accessPath+"/tokens", s.authed(s.handleTokenAdd))
	s.mux.HandleFunc("POST "+accessPath+"/tokens/{id}/delete", s.authed(s.handleTokenDelete))
}

// accessPage is the data of access.html.
type accessPage struct {
	Tokens   []access.Token
	Rights   []access.Right
	NewToken string // the value, shown this once
	NewName  string
	Error    string // catalog key
}

func (s *Server) accessView(errKey string) (accessPage, error) {
	tokens, err := s.deps.Access.Tokens()
	return accessPage{Tokens: tokens, Rights: access.Rights(), Error: errKey}, err
}

func (s *Server) showAccess(w http.ResponseWriter, r *http.Request, sess *session, status int, p accessPage) {
	// A token value on the page must not stay in a cache or history.
	w.Header().Set("Cache-Control", "no-store")
	s.page(w, r, sess, "access", status, p)
}

func (s *Server) handleAccess(w http.ResponseWriter, r *http.Request, sess *session) {
	p, err := s.accessView("")
	if err != nil {
		s.log.Error("tokens", "err", err)
		s.fail(w, r, sess, http.StatusInternalServerError, errKeyInternal)
		return
	}
	s.showAccess(w, r, sess, http.StatusOK, p)
}

// handleTokenAdd makes a token and shows its value on the answer itself,
// not on a redirect: the value never sits in a URL or a second request.
func (s *Server) handleTokenAdd(w http.ResponseWriter, r *http.Request, sess *session) {
	if err := r.ParseForm(); err != nil {
		s.fail(w, r, sess, http.StatusBadRequest, "error.bad_request")
		return
	}

	name := r.PostForm.Get(fieldName)
	rights, valid := access.ParseRights(r.PostForm[fieldRight])

	p, err := s.accessView("")
	if err != nil {
		s.log.Error("tokens", "err", err)
		s.fail(w, r, sess, http.StatusInternalServerError, errKeyInternal)
		return
	}

	switch {
	case !valid || len(rights) == 0:
		p.Error = "access.rights_needed"
	case name == "" || len(name) > maxTokenName:
		p.Error = "access.name_needed"
	}
	if p.Error != "" {
		p.NewName = name
		s.showAccess(w, r, sess, http.StatusBadRequest, p)
		return
	}

	value, err := s.deps.Access.CreateToken(name, rights)
	if err != nil {
		s.log.Error("token create", "err", err)
		s.fail(w, r, sess, http.StatusInternalServerError, errKeyInternal)
		return
	}

	// The list is read again: it holds the new token too.
	p, err = s.accessView("")
	if err != nil {
		s.log.Error("tokens", "err", err)
		s.fail(w, r, sess, http.StatusInternalServerError, errKeyInternal)
		return
	}
	p.NewToken, p.NewName = value, name
	s.showAccess(w, r, sess, http.StatusOK, p)
}

func (s *Server) handleTokenDelete(w http.ResponseWriter, r *http.Request, sess *session) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		s.fail(w, r, sess, http.StatusNotFound, "error.not_found")
		return
	}

	if err := s.deps.Access.DeleteToken(id); err != nil {
		s.log.Error("token delete", "err", err)
		s.fail(w, r, sess, http.StatusInternalServerError, errKeyInternal)
		return
	}
	http.Redirect(w, r, accessPath, http.StatusSeeOther)
}
