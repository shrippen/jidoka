package web

import "net/http"

func (s *Server) registerHome() {
	s.mux.HandleFunc("GET /{$}", s.authed(s.handleHome))
	s.mux.HandleFunc("/", s.authed(s.handleNotFound))
}

// handleHome is the board: the placeholder until the board page replaces it.
func (s *Server) handleHome(w http.ResponseWriter, r *http.Request, sess *session) {
	s.page(w, r, sess, "board", http.StatusOK, nil)
}

// handleNotFound answers every path no route took. Behind the login, so a
// stranger learns nothing about which paths exist.
func (s *Server) handleNotFound(w http.ResponseWriter, r *http.Request, sess *session) {
	s.fail(w, r, sess, http.StatusNotFound, "error.not_found")
}
