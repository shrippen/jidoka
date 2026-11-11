// Adapted from andon internal/i18n/catalog_test.go (468474a).

package i18n

import (
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"
)

// Keys used literally in templates, e.g. {{t "nav.board"}}.
var templateKey = regexp.MustCompile(`\bt\s+"([a-z0-9_.]+)"`)

// codeKey: keys given to T or i18n.T in Go code, e.g. T("x.y", ...).
var codeKey = regexp.MustCompile(`\bT\(\s*"([a-z0-9_.]+)"`)

// srcRoot is internal/.
const srcRoot = "../"

// TestCatalogsHaveSameKeys: a key missing in one language shows the
// fallback (or the raw key) to every user of that language.
func TestCatalogsHaveSameKeys(t *testing.T) {
	ensureLoaded()
	de, en := catalogs[LocaleDE], catalogs[LocaleEN]
	if missing := missingIn(en, de); len(missing) > 0 {
		t.Errorf("only in de.yml: %v", missing)
	}
	if missing := missingIn(de, en); len(missing) > 0 {
		t.Errorf("only in en.yml: %v", missing)
	}
}

// TestUsedKeysExist: every literal key in templates and web code exists.
func TestUsedKeysExist(t *testing.T) {
	ensureLoaded()
	de := catalogs[LocaleDE]
	used := map[string]bool{}

	err := filepath.WalkDir(srcRoot, func(path string, e fs.DirEntry, err error) error {
		if err != nil || e.IsDir() || strings.HasSuffix(path, "_test.go") {
			return err
		}
		if !strings.Contains(path, "/web/") {
			return nil
		}

		switch filepath.Ext(path) {
		case ".html":
			collect(t, path, templateKey, used)
		case ".go":
			collect(t, path, codeKey, used)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}

	var missing []string
	for key := range used {
		if _, ok := de[key]; !ok {
			missing = append(missing, key)
		}
	}
	sort.Strings(missing)
	if len(missing) > 0 {
		t.Errorf("keys used but not in the catalog: %v", missing)
	}
}

func collect(t *testing.T, path string, re *regexp.Regexp, into map[string]bool) {
	t.Helper()
	raw, err := readFile(path)
	if err != nil {
		t.Fatal(err)
	}
	for _, m := range re.FindAllStringSubmatch(raw, -1) {
		into[m[1]] = true
	}
}

func missingIn(have, want map[string]string) []string {
	var out []string
	for key := range want {
		if _, ok := have[key]; !ok {
			out = append(out, key)
		}
	}
	sort.Strings(out)
	return out
}

func readFile(path string) (string, error) {
	b, err := os.ReadFile(path)
	return string(b), err
}
