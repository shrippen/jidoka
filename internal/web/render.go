package web

import (
	"bytes"
	"embed"
	"fmt"
	"html/template"
	"io/fs"
	"net/http"
	"strings"
	"time"

	"jidoka/internal/i18n"
	"jidoka/internal/services/access"
)

//go:embed templates/*.html
var templateFiles embed.FS

const baseTemplate = "base.html"

// pages holds one template set per page: base.html plus the page's file.
// A page defines "title" and "main"; html/template cannot include a
// template by a name known only at run time, so each page carries its
// own copy of the frame.
//
//	base.html ─ {{template "title"}} ─┐
//	          └ {{template "main"}}  ─┴─► page file (login.html, access.html, …)
var pages = mustParse()

// placeholders let the templates parse before the locale-bound funcs
// are set per render (see bind).
func placeholders() template.FuncMap {
	return template.FuncMap{
		"t":      func(string, ...any) string { return "" },
		"stamp":  func(time.Time) string { return "" },
		"tzMark": func() template.HTML { return "" },
		"asset":  asset,
	}
}

func mustParse() map[string]*template.Template {
	base := template.Must(template.New(baseTemplate).Funcs(placeholders()).ParseFS(templateFiles, "templates/"+baseTemplate))

	files, err := fs.ReadDir(templateFiles, "templates")
	if err != nil {
		panic(err)
	}

	out := map[string]*template.Template{}
	for _, f := range files {
		if f.Name() == baseTemplate {
			continue
		}

		set := template.Must(base.Clone())
		out[strings.TrimSuffix(f.Name(), ".html")] = template.Must(set.ParseFS(templateFiles, "templates/"+f.Name()))
	}
	return out
}

// tab is one entry of the header's navigation.
type tab struct {
	Key    string
	Path   string
	Active bool
}

// tabs are the pages of Jidoka. The Plan, Channels and Secrets pages
// come from their own routes_*.go.
var tabs = []tab{
	{Key: "board", Path: "/"},
	{Key: "plan", Path: "/plan"},
	{Key: "channels", Path: "/channels"},
	{Key: "secrets", Path: "/secrets"},
	{Key: "access", Path: "/access"},
}

// view is what base.html and every page read.
type view struct {
	Locale   i18n.Locale
	Who      *access.Who
	CSRF     string
	Tabs     []tab
	AndonURL string
	Version  string
	Data     any // the page's own data
}

// currentTabs marks the tab of path: "/" only itself, the others and
// everything below them.
func currentTabs(path string) []tab {
	out := make([]tab, len(tabs))
	for i, t := range tabs {
		t.Active = path == t.Path || (t.Path != homePath && strings.HasPrefix(path, t.Path+"/"))
		out[i] = t
	}
	return out
}

// bind sets the funcs that depend on the request: language and zone.
func bind(set *template.Template, locale i18n.Locale, zone *time.Location) *template.Template {
	return set.Funcs(template.FuncMap{
		// t "key" "name" value …: the text with {name} replaced.
		"t": func(key string, kv ...any) string {
			params := map[string]any{}
			for i := 0; i+1 < len(kv); i += 2 {
				params[fmt.Sprint(kv[i])] = kv[i+1]
			}
			return i18n.T(key, locale, params)
		},
		// stamp is a time as date and clock in Jidoka's zone; tzMark follows it.
		"stamp": func(t time.Time) string {
			return i18n.Day(t.In(zone), locale) + " " + t.In(zone).Format("15:04")
		},
		"tzMark": func() template.HTML { return tzMark(zone) },
	})
}

// page renders template name with data into w. sess is nil on pages
// before the login.
func (s *Server) page(w http.ResponseWriter, r *http.Request, sess *session, name string, status int, data any) {
	set, ok := pages[name]
	if !ok {
		s.log.Error("render", "template", name, "err", "unknown page")
		http.Error(w, http.StatusText(http.StatusInternalServerError), http.StatusInternalServerError)
		return
	}

	cfg := s.settings()
	v := view{
		Locale: localeOf(r), Tabs: currentTabs(r.URL.Path),
		AndonURL: cfg.AndonURL, Version: s.deps.Version, Data: data,
	}
	if sess != nil {
		v.Who, v.CSRF = &sess.Who, sess.csrf()
	}

	// Rendered into memory first: a failing template is a clean 500, not
	// half a page.
	var buf bytes.Buffer
	clone, err := set.Clone()
	if err == nil {
		err = bind(clone, v.Locale, cfg.Zone).ExecuteTemplate(&buf, baseTemplate, v)
	}
	if err != nil {
		s.log.Error("render", "template", name, "err", err)
		http.Error(w, http.StatusText(http.StatusInternalServerError), http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(status)
	_, _ = w.Write(buf.Bytes())
}

// errorPage is the data of error.html.
type errorPage struct {
	Status int
	Key    string
}

// fail shows an error page with a translated message.
func (s *Server) fail(w http.ResponseWriter, r *http.Request, sess *session, status int, key string) {
	s.page(w, r, sess, "error", status, errorPage{Status: status, Key: key})
}
