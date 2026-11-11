// Package conf loads /jobs (jidoka.toml, <id>/job.toml, <id>/watch.toml)
// into a model.Set. Pure apart from reading files; never panics on bad
// input. A broken job never stops the others (J2).
//
//	<dir>/jidoka.toml           -> Set.Settings (broken: keep prev, B8)
//	<dir>/<id>/job.toml         -> Set.Jobs[id]    (broken: Set.Invalid)
//	<dir>/<id>/watch.toml       -> Set.Jobs[id]
//	<dir>/.git/HEAD             -> Set.Commit
package conf

import (
	"errors"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/BurntSushi/toml"

	"jidoka/internal/i18n"
	"jidoka/internal/model"
)

const (
	settingsFile = "jidoka.toml"
	jobFile      = "job.toml"
	watchFile    = "watch.toml"
)

// Need says whether a missing jidoka.toml is an error.
type Need int

const (
	// SettingsRequired: the server; without the file there are no channels
	// and no messages (B8).
	SettingsRequired Need = iota
	// SettingsOptional: `jidoka run` tries single jobs without it.
	SettingsOptional
)

// Load reads dir for the server: jidoka.toml is required. prev is the last
// valid Settings, kept if jidoka.toml is broken or missing; nil means
// defaults.
func Load(dir string, prev *model.Settings) model.Set {
	return LoadWith(dir, prev, SettingsRequired)
}

// LoadWith is Load with a choice about a missing jidoka.toml.
func LoadWith(dir string, prev *model.Settings, need Need) model.Set {
	set := model.Set{
		Jobs:     map[string]*model.Job{},
		Commit:   readCommit(dir),
		LoadedAt: time.Now().UTC(),
	}

	entries, err := os.ReadDir(dir)
	if err != nil {
		// One error for the folder; its settings file is no second one.
		set.Settings, set.SettingsFrom = fallbackSettings(prev)
		set.Invalid = append(set.Invalid, loadError("", model.DirFile, 0, i18n.M("conf.io", "detail", err.Error())))
		return set
	}

	set.Settings, set.SettingsErr, set.SettingsFrom = loadSettings(dir, prev, need)

	ctx := loadCtx{dir: dir, set: &set, now: set.LoadedAt}
	for _, e := range entries {
		if !e.IsDir() || strings.HasPrefix(e.Name(), ".") {
			continue
		}
		ctx.loadEntry(e.Name())
	}

	sort.Slice(set.Invalid, func(a, b int) bool {
		if set.Invalid[a].File != set.Invalid[b].File {
			return set.Invalid[a].File < set.Invalid[b].File
		}
		return set.Invalid[a].Line < set.Invalid[b].Line
	})

	return set
}

// loadSettings reads jidoka.toml; on any error it returns the fallback.
func loadSettings(dir string, prev *model.Settings, need Need) (model.Settings, *model.LoadError, model.SettingsSource) {
	fallback, from := fallbackSettings(prev)

	src, err := os.ReadFile(filepath.Join(dir, settingsFile))
	if errors.Is(err, os.ErrNotExist) && need == SettingsOptional {
		return fallback, nil, from
	}
	if errors.Is(err, os.ErrNotExist) {
		le := loadError("", settingsFile, 0, i18n.M("conf.no_settings"))
		return fallback, &le, from
	}
	if err != nil {
		le := loadError("", settingsFile, 0, i18n.M("conf.io", "detail", err.Error()))
		return fallback, &le, from
	}

	s, lerr := ParseSettings(src)
	if lerr != nil {
		return fallback, lerr, from
	}

	return s, nil, model.SettingsFile
}

// fallbackSettings is what stays in force when jidoka.toml does not load:
// the last valid copy, else the defaults.
func fallbackSettings(prev *model.Settings) (model.Settings, model.SettingsSource) {
	if prev != nil {
		return *prev, model.SettingsSaved
	}

	return defaults(), model.SettingsDefaults
}

// ParseSettings parses the content of a jidoka.toml. Callers keep the
// bytes of the last valid file and re-parse them when the file breaks (B8).
func ParseSettings(src []byte) (model.Settings, *model.LoadError) {
	var raw rawSettings
	is := decode(newIssues("", settingsFile, string(src)), string(src), &raw)
	if len(is.list) > 0 {
		return defaults(), &is.list[0]
	}

	s := buildSettings(is, raw)
	if len(is.list) > 0 {
		return defaults(), &is.list[0]
	}

	return s, nil
}

// Commit returns the short commit of <dir>/.git/HEAD, "" if unknown.
func Commit(dir string) string {
	return readCommit(dir)
}

// decode strictly decodes src into v and records syntax errors, type
// errors and unknown keys in is.
func decode(is *issues, src string, v any) *issues {
	md, err := toml.Decode(src, v)
	if err != nil {
		var pe toml.ParseError
		if errors.As(err, &pe) {
			is.atLine(pe.Position.Line, i18n.M("conf.syntax", "detail", pe.Message))
			return is
		}
		line, detail := typeErrLine(err)
		is.atLine(line, i18n.M("conf.type", "detail", detail))
		return is
	}

	for _, key := range md.Undecoded() {
		sect := strings.Join(key[:len(key)-1], ".")
		is.at(sect, anyIdx, key[len(key)-1], i18n.M("conf.unknown_key", "key", key.String()))
	}

	return is
}

// typeErrRe matches the library's type errors, which are plain errors:
// "toml: line 3 (last key "timeout"): incompatible types: ..."
var typeErrRe = regexp.MustCompile(`^toml: (?:line (\d+) )?(?:\(last key "[^"]*"\): )?(.*)$`)

// typeErrLine splits such an error into line (0 if absent) and message.
func typeErrLine(err error) (int, string) {
	m := typeErrRe.FindStringSubmatch(err.Error())
	if m == nil {
		return 0, err.Error()
	}
	line, _ := strconv.Atoi(m[1])

	return line, m[2]
}

// loadCtx carries what loading job folders needs.
type loadCtx struct {
	dir string
	set *model.Set
	now time.Time
}

// loadEntry loads one folder, if it holds a job.toml or watch.toml.
func (c loadCtx) loadEntry(id string) {
	jobPath := filepath.Join(c.dir, id, jobFile)
	watchPath := filepath.Join(c.dir, id, watchFile)
	hasJob, hasWatch := exists(jobPath), exists(watchPath)

	if !hasJob && !hasWatch {
		return
	}

	file := id + "/" + jobFile
	if !hasJob {
		file = id + "/" + watchFile
	}

	if !idRe.MatchString(id) {
		c.fail(id, file, 0, i18n.M("conf.folder", "name", id))
		return
	}
	if hasJob && hasWatch {
		c.fail(id, file, 0, i18n.M("conf.both_files"))
		return
	}

	src, err := os.ReadFile(filepath.Join(c.dir, file))
	if err != nil {
		c.fail(id, file, 0, i18n.M("conf.io", "detail", err.Error()))
		return
	}

	is := newIssues(id, file, string(src))
	j := &model.Job{ID: id, File: file}
	j.Dir, _ = filepath.Abs(filepath.Join(c.dir, id))

	jc := jobCtx{is: is, set: &c.set.Settings, now: c.now}
	if hasJob {
		var raw rawJob
		decode(is, string(src), &raw)
		if len(is.list) == 0 {
			jc.buildJob(j, filepath.Join(c.dir, id), raw)
		}
	} else {
		var raw rawWatch
		decode(is, string(src), &raw)
		if len(is.list) == 0 {
			jc.buildWatch(j, raw)
		}
	}

	if len(is.list) > 0 {
		c.set.Invalid = append(c.set.Invalid, is.list...)
		return
	}

	hash, err := hashDir(filepath.Join(c.dir, id))
	if err != nil {
		c.fail(id, file, 0, i18n.M("conf.io", "detail", err.Error()))
		return
	}
	j.Hash = hash
	c.set.Jobs[id] = j
}

func (c loadCtx) fail(id, file string, line int, msg i18n.Msg) {
	c.set.Invalid = append(c.set.Invalid, loadError(id, file, line, msg))
}

func exists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}
