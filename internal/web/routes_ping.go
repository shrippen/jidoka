package web

import (
	"errors"
	"io"
	"net/http"
	"net/netip"
	"strings"

	"jidoka/internal/services/watch"
)

// Ping routes of external watches (W2, W3), public: the token is the key.
//
//	POST|GET /ping/{token}         ok     body (up to 10 KB) becomes the output
//	POST|GET /ping/{token}/start   run begins
//	POST|GET /ping/{token}/fail    run failed
//
// The log shows the route pattern, never the path: the token stays out.
const (
	pingPath     = "/ping/{token}"
	pingBodyMax  = 10 << 10
	retryAfter   = "1"
	headerXFF    = "X-Forwarded-For"
	pingOKAnswer = "ok\n"
	textPlain    = "text/plain; charset=utf-8"
)

func (s *Server) registerPing() {
	for _, p := range []struct {
		suffix string
		kind   watch.PingKind
	}{{"", watch.PingOK}, {"/start", watch.PingStart}, {"/fail", watch.PingFail}} {
		h := s.handlePing(p.kind)
		s.mux.HandleFunc("POST "+pingPath+p.suffix, h)
		s.mux.HandleFunc("GET "+pingPath+p.suffix, h)
	}
}

func (s *Server) handlePing(kind watch.PingKind) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if s.deps.Watch == nil {
			http.NotFound(w, r)
			return
		}

		// A GET has no body; a POST body beyond the cap is not read.
		var body string
		if r.Method == http.MethodPost {
			raw, err := io.ReadAll(io.LimitReader(r.Body, pingBodyMax))
			if err != nil {
				http.Error(w, http.StatusText(http.StatusBadRequest), http.StatusBadRequest)
				return
			}
			body = strings.ToValidUTF8(string(raw), "�")
		}

		err := s.deps.Watch.Ping(r.PathValue("token"), kind, body, s.clientAddr(r))
		switch {
		case errors.Is(err, watch.ErrUnknown):
			http.NotFound(w, r)
		case errors.Is(err, watch.ErrThrottled):
			w.Header().Set("Retry-After", retryAfter)
			http.Error(w, http.StatusText(http.StatusTooManyRequests), http.StatusTooManyRequests)
		case err != nil:
			s.log.Error("ping", "route", routeOf(r), "err", err)
			http.Error(w, http.StatusText(http.StatusInternalServerError), http.StatusInternalServerError)
		default:
			w.Header().Set("Content-Type", textPlain)
			_, _ = io.WriteString(w, pingOKAnswer)
		}
	}
}

// parseProxies reads TRUSTED_PROXY: CIDRs or single addresses, comma
// separated. A bad entry is logged and skipped, so a typo trusts nobody.
func (s *Server) parseProxies(list string) []netip.Prefix {
	var out []netip.Prefix
	for _, item := range strings.Split(list, ",") {
		item = strings.TrimSpace(item)
		if item == "" {
			continue
		}

		p, err := netip.ParsePrefix(item)
		if err != nil {
			a, aerr := netip.ParseAddr(item)
			if aerr != nil {
				s.log.Warn("TRUSTED_PROXY: bad entry skipped", "entry", item)
				continue
			}
			p = netip.PrefixFrom(a.Unmap(), a.Unmap().BitLen())
		}
		out = append(out, p.Masked())
	}
	return out
}

func (s *Server) trusted(a netip.Addr) bool {
	for _, p := range s.proxies {
		if p.Contains(a) {
			return true
		}
	}
	return false
}

// clientAddr is the address the rate limit counts: the peer, or behind a
// trusted proxy the first untrusted address of X-Forwarded-For, read from
// the right (a client can put anything on the left):
//
//	peer 10.0.0.2 (trusted)  XFF: "6.6.6.6, 203.0.113.9, 10.0.0.2"  ->  203.0.113.9
//	peer 198.51.100.4        XFF: "6.6.6.6"                          ->  198.51.100.4
func (s *Server) clientAddr(r *http.Request) string {
	ap, err := netip.ParseAddrPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}

	peer := ap.Addr().Unmap()
	if !s.trusted(peer) {
		return peer.String()
	}

	var hops []string
	for _, line := range r.Header.Values(headerXFF) {
		hops = append(hops, strings.Split(line, ",")...)
	}

	for i := len(hops) - 1; i >= 0; i-- {
		a, err := netip.ParseAddr(strings.TrimSpace(hops[i]))
		if err != nil {
			break
		}
		if a = a.Unmap(); !s.trusted(a) {
			return a.String()
		}
	}
	return peer.String()
}
