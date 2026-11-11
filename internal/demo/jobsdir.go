//go:build !release

package demo

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/BurntSushi/toml"
)

const (
	jobFile    = "job.toml"
	watchFile  = "watch.toml"
	scriptFile = "run.sh"

	dirMode    = 0o755
	fileMode   = 0o644
	scriptMode = 0o755

	// Prefixes of a script line that starts with the time of day.
	prefixStart = placeStart + " "
	prefixEnd   = placeEnd + " "
)

// writeJobs writes one folder per job: job.toml (or watch.toml) from the
// world's config, and for jobs a small script.
//
//	<dir>/<id>/job.toml + run.sh     <dir>/<id>/watch.toml
func writeJobs(dir string, d data) error {
	if err := os.MkdirAll(dir, dirMode); err != nil {
		return err
	}
	for _, def := range d.Jobs {
		if err := writeJob(filepath.Join(dir, def.ID), def); err != nil {
			return fmt.Errorf("job %s: %w", def.ID, err)
		}
	}

	return nil
}

func writeJob(dir string, def jobDef) error {
	if err := os.MkdirAll(dir, dirMode); err != nil {
		return err
	}

	if def.Kind == kindWatch {
		return writeTOML(filepath.Join(dir, watchFile), def.Config)
	}

	if err := writeTOML(filepath.Join(dir, jobFile), def.Config); err != nil {
		return err
	}

	return os.WriteFile(filepath.Join(dir, scriptFile), []byte(scriptText(def.Script)), scriptMode)
}

func writeTOML(path string, v map[string]any) error {
	doc, err := tomlDoc(v)
	if err != nil {
		return err
	}

	return os.WriteFile(path, doc, fileMode)
}

// tomlDoc is v as a TOML document.
func tomlDoc(v map[string]any) ([]byte, error) {
	var buf bytes.Buffer
	if err := toml.NewEncoder(&buf).Encode(tomlValue(v)); err != nil {
		return nil, err
	}

	return buf.Bytes(), nil
}

// scriptText is a tiny script: it sleeps, prints the world's lines,
// writes the summary and exits with the world's code.
func scriptText(s scriptDef) string {
	var sb strings.Builder

	sb.WriteString("#!/bin/sh\n")
	fmt.Fprintf(&sb, "sleep %d\n", s.Sleep)

	for _, line := range s.Output {
		sb.WriteString(printLine(line))
	}
	if s.Summary != "" {
		fmt.Fprintf(&sb, "printf '%%s\\n' %s > \"$JIDOKA_SUMMARY\"\n", quote(s.Summary))
	}
	fmt.Fprintf(&sb, "exit %d\n", s.Exit)

	return sb.String()
}

// printLine prints one line; a time placeholder becomes the clock.
func printLine(line string) string {
	for _, p := range []string{prefixStart, prefixEnd} {
		if rest, ok := strings.CutPrefix(line, p); ok {
			return fmt.Sprintf("printf '%%s %%s\\n' \"$(date +%%T)\" %s\n", quote(rest))
		}
	}

	return fmt.Sprintf("printf '%%s\\n' %s\n", quote(line))
}

// quote makes s one word for sh.
func quote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}
