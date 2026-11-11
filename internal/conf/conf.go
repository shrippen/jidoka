// Package conf loads the job folders of /jobs (<id>/job.toml,
// <id>/watch.toml) into a model.Set, and parses and checks the settings
// document. Pure apart from reading files; never panics on bad input. A
// broken job never stops the others (J2).
//
//	Settings ─────────────────────┐
//	<dir>/<id>/job.toml           ├─► Set.Jobs[id]   (broken: Set.Invalid)
//	<dir>/<id>/watch.toml         ┘
//	<dir>/.git/HEAD               ──► Set.Commit
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
	jobFile   = "job.toml"
	watchFile = "watch.toml"
)

// Check says how strictly a job is held to the settings.
type Check int

const (
	// CheckFull: the server; a job may only name profiles and Apprise tags
	// the settings define.
	CheckFull Check = iota
	// CheckOffline: `jidoka validate` and `jidoka run` know no stored
	// settings; profile names and sub channels are checked on the server.
	CheckOffline
)

// Load reads the job folders of dir and builds them against s.
func Load(dir string, s model.Settings, check Check) model.Set {
	set := model.Set{
		Settings: s,
		Jobs:     map[string]*model.Job{},
		Commit:   readCommit(dir),
		LoadedAt: time.Now().UTC(),
	}

	entries, err := os.ReadDir(dir)
	if err != nil {
		set.Invalid = append(set.Invalid, loadError("", model.DirFile, 0, i18n.M("conf.io", "detail", err.Error())))
		return set
	}

	ctx := loadCtx{dir: dir, set: &set, now: set.LoadedAt, check: check}
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
	dir   string
	set   *model.Set
	now   time.Time
	check Check
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

	jc := jobCtx{is: is, set: &c.set.Settings, now: c.now, check: c.check}
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
