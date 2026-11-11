// Package redact masks secret values in text (J7). It is damage control,
// not a security boundary: only the plain value and a few common
// encodings are found.
package redact

import (
	"encoding/base64"
	"net/url"
	"sort"
	"strings"
)

const (
	// Shorter values would mask ordinary text.
	minLen = 6
	mask   = "***"
)

// Masker replaces secrets and their encoded forms.
type Masker struct {
	repl *strings.Replacer
}

// New builds a Masker. Values shorter than 6 bytes are ignored.
func New(values []string) *Masker {
	seen := map[string]bool{}
	var forms []string

	add := func(f string) {
		if len(f) < minLen || seen[f] {
			return
		}
		seen[f] = true
		forms = append(forms, f)
	}

	for _, v := range values {
		if len(v) < minLen {
			continue
		}
		b := []byte(v)
		add(v)
		add(base64.StdEncoding.EncodeToString(b))
		add(base64.RawStdEncoding.EncodeToString(b))
		add(base64.URLEncoding.EncodeToString(b))
		add(base64.RawURLEncoding.EncodeToString(b))
		add(url.QueryEscape(v))
		add(url.PathEscape(v))
	}

	// Longest first: strings.Replacer prefers the earlier pair on a tie
	// at one position, so a long form wins over a prefix of it.
	sort.SliceStable(forms, func(i, j int) bool { return len(forms[i]) > len(forms[j]) })

	pairs := make([]string, 0, 2*len(forms))
	for _, f := range forms {
		pairs = append(pairs, f, mask)
	}

	return &Masker{repl: strings.NewReplacer(pairs...)}
}

// String returns s with every known secret form replaced by ***.
func (m *Masker) String(s string) string {
	if m == nil {
		return s
	}

	return m.repl.Replace(s)
}
