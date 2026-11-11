package conf

import (
	"errors"
	"strconv"
	"strings"

	"jidoka/internal/i18n"
	"jidoka/internal/model"
)

// anyIdx matches any occurrence of an array table such as [[notify]].
const anyIdx = -1

// profilesSect is the section of the profiles in the settings document.
const profilesSect = "profiles"

// issues collects the errors of one file and finds their line numbers.
type issues struct {
	job   string
	file  string
	lines []string
	list  []model.LoadError
}

func newIssues(job, file, src string) *issues {
	return &issues{job: job, file: file, lines: strings.Split(src, "\n")}
}

// at records an error for key in section sect. idx picks the n-th
// occurrence of an array table ([[notify]] #2 is idx 1), or anyIdx.
func (is *issues) at(sect string, idx int, key string, msg i18n.Msg) {
	is.atLine(findLine(is.lines, sect, idx, key), msg)
	is.list[len(is.list)-1].Field = fieldOf(sect, idx, key)
}

// fieldOf names the key of an error. A rule of a profile also names its
// position, as the profile editor needs it: "profiles.leise.1.after".
func fieldOf(sect string, idx int, key string) string {
	if idx == anyIdx || !strings.HasPrefix(sect, profilesSect+".") {
		return joinKey(sect, key)
	}

	return joinKey(sect+"."+strconv.Itoa(idx), key)
}

// atLine records an error with a known line (TOML syntax errors).
func (is *issues) atLine(line int, msg i18n.Msg) {
	is.list = append(is.list, loadError(is.job, is.file, line, msg))
}

// atErr records an error of a parse helper: an i18n.Msg as it is,
// anything else as its text.
func (is *issues) atErr(sect string, idx int, key string, err error) {
	is.at(sect, idx, key, msgOf(err))
}

// msgOf is err as a Msg.
func msgOf(err error) i18n.Msg {
	var m i18n.Msg
	if errors.As(err, &m) {
		return m
	}

	return i18n.M("conf.other", "detail", err.Error())
}

// loadError builds the error with its English text.
func loadError(job, file string, line int, m i18n.Msg) model.LoadError {
	return model.LoadError{Job: job, File: file, Line: line, Key: m.Key, Params: m.Params, Msg: m.Error()}
}

// findLine locates `key =` inside a section, 1-based; 0 if unknown.
// Falls back to the section header, then to a header named like the key
// (an unknown sub-table). A scan is enough: the TOML library gives
// positions only for syntax errors.
//
//	[[notify]]      <- section "notify", occurrence 0
//	on = ["fail"]   <- key "on"
func findLine(lines []string, sect string, idx int, key string) int {
	cur, curOcc := "", 0
	seen := map[string]int{}
	header, tableMatch := 0, 0

	for i, raw := range lines {
		t := strings.TrimSpace(raw)
		if t == "" || strings.HasPrefix(t, "#") {
			continue
		}

		if strings.HasPrefix(t, "[") {
			cur = headerName(t)
			curOcc = seen[cur]
			seen[cur]++

			if cur == sect && (idx == anyIdx || idx == curOcc) && header == 0 {
				header = i + 1
			}
			if key != "" && cur == joinKey(sect, key) && tableMatch == 0 {
				tableMatch = i + 1
			}
			continue
		}

		if cur != sect || (idx != anyIdx && idx != curOcc) {
			continue
		}
		if keyLine(t, key) {
			return i + 1
		}
	}

	if tableMatch != 0 {
		return tableMatch
	}

	return header
}

// headerName turns "[[profiles.standard]]  # x" into "profiles.standard".
func headerName(t string) string {
	t, _, _ = strings.Cut(t, "#")

	return strings.TrimSpace(strings.Trim(strings.TrimSpace(t), "[]"))
}

func joinKey(sect, key string) string {
	if sect == "" {
		return key
	}

	return sect + "." + key
}

// keyLine reports whether the trimmed line assigns key.
func keyLine(t, key string) bool {
	t = strings.TrimPrefix(t, `"`)
	if !strings.HasPrefix(t, key) {
		return false
	}
	rest := strings.TrimLeft(strings.TrimPrefix(t, key), `" `+"\t")

	return strings.HasPrefix(rest, "=")
}
