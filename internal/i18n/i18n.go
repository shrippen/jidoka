// Adapted from andon internal/i18n/i18n.go (468474a).

// Package i18n loads the dotted-key YAML catalogs and formats dates for
// German and English.
//
//	T("nav.board", LocaleEN, nil) -> "Board"
package i18n

import (
	"embed"
	"fmt"
	"regexp"
	"strings"
	"sync"
	"time"

	"gopkg.in/yaml.v3"
)

// Locale is a supported UI language.
type Locale string

const (
	LocaleDE Locale = "de"
	LocaleEN Locale = "en"
)

// DefaultLocale is the fallback for a missing key or language.
const DefaultLocale = LocaleDE

//go:embed catalogs/*.yml
var catalogFiles embed.FS

var (
	catalogs = map[Locale]map[string]string{}
	loadOnce sync.Once
)

// ensureLoaded parses the catalogs on first use; requests translate in
// parallel, so the load must happen exactly once.
func ensureLoaded() {
	loadOnce.Do(load)
}

func load() {
	for _, loc := range []Locale{LocaleDE, LocaleEN} {
		raw, err := catalogFiles.ReadFile("catalogs/" + string(loc) + ".yml")
		if err != nil {
			panic(fmt.Sprintf("i18n: missing catalog for %s: %v", loc, err))
		}

		var node map[string]any
		if err := yaml.Unmarshal(raw, &node); err != nil {
			panic(fmt.Sprintf("i18n: bad catalog for %s: %v", loc, err))
		}

		flat := map[string]string{}
		flatten("", node, flat)
		catalogs[loc] = flat
	}
}

func flatten(prefix string, node any, out map[string]string) {
	m, ok := node.(map[string]any)
	if !ok {
		out[prefix] = fmt.Sprint(node)
		return
	}

	for key, value := range m {
		full := key
		if prefix != "" {
			full = prefix + "." + key
		}
		flatten(full, value, out)
	}
}

var placeholder = regexp.MustCompile(`\{([a-zA-Z0-9_]+)\}`)

// singular is the suffix of a key's text for a count of one.
const singular = "_singular"

// T looks up key in locale's catalog (falling back to DefaultLocale, then
// the raw key), substituting params. Unknown placeholders stay visible as
// "{name}" rather than failing.
//
// A count of one takes "<key>_singular" when the catalog has it:
// "{count} Läufe" -> "1 Lauf".
func T(key string, locale Locale, params map[string]any) string {
	ensureLoaded()
	if isOne(params["count"]) {
		if _, ok := catalogs[DefaultLocale][key+singular]; ok {
			key += singular
		}
	}

	text, ok := catalogs[locale][key]
	if !ok {
		text, ok = catalogs[DefaultLocale][key]
	}
	if !ok {
		text = key
	}
	if len(params) == 0 {
		return text
	}

	return placeholder.ReplaceAllStringFunc(text, func(m string) string {
		v, ok := params[m[1:len(m)-1]]
		if !ok {
			return m
		}
		return fmt.Sprint(v)
	})
}

// isOne: the count is the number one, of whatever numeric type.
func isOne(v any) bool {
	switch n := v.(type) {
	case int:
		return n == 1
	case int64:
		return n == 1
	case float64:
		return n == 1
	}
	return false
}

// Has reports whether key is in the default catalog.
func Has(key string) bool {
	ensureLoaded()
	_, ok := catalogs[DefaultLocale][key]
	return ok
}

// langLen is the length of a language code ("de" of "de-DE").
const langLen = 2

// Pick returns the first supported language of an Accept-Language header,
// or DefaultLocale.
func Pick(acceptLanguage string) Locale {
	for _, part := range strings.Split(acceptLanguage, ",") {
		code := strings.ToLower(strings.TrimSpace(strings.Split(part, ";")[0]))
		if len(code) > langLen {
			code = code[:langLen]
		}

		switch Locale(code) {
		case LocaleDE:
			return LocaleDE
		case LocaleEN:
			return LocaleEN
		}
	}
	return DefaultLocale
}

// Day formats a date as a medium localized date: "05.03.2026" or
// "Mar 5, 2026".
func Day(t time.Time, locale Locale) string {
	if locale == LocaleDE {
		return t.Format("02.01.2006")
	}
	return t.Format("Jan 2, 2006")
}
