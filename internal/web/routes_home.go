package web

func (s *Server) registerHome() {
	s.mux.HandleFunc("/", s.authed(s.handleNotFound))
}
