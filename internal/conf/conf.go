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
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/BurntSushi/toml"

	"jidoka/internal/model"
)

const (
	settingsFile = "jidoka.toml"
	jobFile      = "job.toml"
	watchFile    = "watch.toml"
)

// Load reads dir. prev is the last valid Settings, kept if jidoka.toml is
// broken; nil means defaults.
func Load(dir string, prev *model.Settings) model.Set {
	set := model.Set{
		Jobs:     map[string]*model.Job{},
		Commit:   readCommit(dir),
		LoadedAt: time.Now().UTC(),
	}

	set.Settings, set.SettingsErr = loadSettings(dir, prev)

	entries, err := os.ReadDir(dir)
	if err != nil {
		set.Invalid = append(set.Invalid, model.LoadError{File: ".", Msg: err.Error()})
		return set
	}

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
func loadSettings(dir string, prev *model.Settings) (model.Settings, *model.LoadError) {
	fallback := defaults()
	if prev != nil {
		fallback = *prev
	}

	src, err := os.ReadFile(filepath.Join(dir, settingsFile))
	if errors.Is(err, os.ErrNotExist) {
		return fallback, nil
	}
	if err != nil {
		return fallback, &model.LoadError{File: settingsFile, Msg: err.Error()}
	}

	var raw rawSettings
	is := decode(newIssues("", settingsFile, string(src)), string(src), &raw)
	if len(is.list) > 0 {
		return fallback, &is.list[0]
	}

	s := buildSettings(is, raw)
	if len(is.list) > 0 {
		return fallback, &is.list[0]
	}

	return s, nil
}

// decode strictly decodes src into v and records syntax errors, type
// errors and unknown keys in is.
func decode(is *issues, src string, v any) *issues {
	md, err := toml.Decode(src, v)
	if err != nil {
		var pe toml.ParseError
		if errors.As(err, &pe) {
			is.atLine(pe.Position.Line, pe.Message)
			return is
		}
		is.atLine(typeErrLine(err))
		return is
	}

	for _, key := range md.Undecoded() {
		sect := strings.Join(key[:len(key)-1], ".")
		is.at(sect, anyIdx, key[len(key)-1], "unknown key %q", key.String())
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
		c.fail(id, file, 0, fmt.Sprintf("folder name %q must match [a-z0-9-]+", id))
		return
	}
	if hasJob && hasWatch {
		c.fail(id, file, 0, "job.toml and watch.toml in one folder")
		return
	}

	src, err := os.ReadFile(filepath.Join(c.dir, file))
	if err != nil {
		c.fail(id, file, 0, err.Error())
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
		c.fail(id, file, 0, err.Error())
		return
	}
	j.Hash = hash
	c.set.Jobs[id] = j
}

func (c loadCtx) fail(id, file string, line int, msg string) {
	c.set.Invalid = append(c.set.Invalid, model.LoadError{Job: id, File: file, Line: line, Msg: msg})
}

func exists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}
