// Package git keeps a read-only checkout of a private repo with the git
// binary (J1).
//
//	Sync:  git init (once) ─► git fetch URL +refs/heads/<b>:refs/remotes/origin/<b>
//	                      ─► git reset --hard origin/<b> ─► git clean ─► modes
//
// The token reaches git only through its environment (http.extraHeader by
// GIT_CONFIG_*), so it is in no URL, no .git/config and no file a job can
// read. The URL is passed on the command line, not stored as a remote.
package git

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

const (
	defaultBin     = "git"
	defaultBranch  = "main"
	defaultTimeout = time.Minute

	// Modes of the checkout: jobs run under other uids and only read it.
	dirMode  = 0o755
	fileMode = 0o644
	exeMode  = 0o755

	// maxErrText keeps an error message short.
	maxErrText = 500
)

// Config describes the checkout.
type Config struct {
	URL     string // https URL of the repo
	Branch  string // default main
	Token   string // sent as "Authorization: token <Token>"; may be empty
	Dir     string // the checkout
	Bin     string // git binary; default "git"
	Timeout time.Duration
}

// Repo is the checkout of one repo.
type Repo struct {
	cfg Config
}

// New returns a Repo; nothing runs until Sync.
func New(cfg Config) *Repo {
	if cfg.Branch == "" {
		cfg.Branch = defaultBranch
	}
	if cfg.Bin == "" {
		cfg.Bin = defaultBin
	}
	if cfg.Timeout <= 0 {
		cfg.Timeout = defaultTimeout
	}

	return &Repo{cfg: cfg}
}

// Sync brings the checkout to origin/<branch>, discarding local changes.
// On failure the files stay as they were.
func (r *Repo) Sync(ctx context.Context) error {
	ctx, cancel := context.WithTimeout(ctx, r.cfg.Timeout)
	defer cancel()

	dir := r.cfg.Dir
	if err := os.MkdirAll(dir, dirMode); err != nil {
		return err
	}
	if err := os.Chmod(dir, dirMode); err != nil {
		return err
	}

	if _, err := os.Stat(filepath.Join(dir, ".git")); errors.Is(err, fs.ErrNotExist) {
		if err := r.git(ctx, "init", "-q", "-b", r.cfg.Branch); err != nil {
			return err
		}
	}

	ref := "refs/remotes/origin/" + r.cfg.Branch
	steps := [][]string{
		{"fetch", "-q", "--depth", "1", "--prune", r.cfg.URL, "+refs/heads/" + r.cfg.Branch + ":" + ref},
		{"reset", "-q", "--hard", ref},
		{"clean", "-q", "-f", "-d", "-x"},
	}
	for _, args := range steps {
		if err := r.git(ctx, args...); err != nil {
			return err
		}
	}

	return openModes(dir)
}

// git runs one git command in the checkout.
func (r *Repo) git(ctx context.Context, args ...string) error {
	cmd := exec.CommandContext(ctx, r.cfg.Bin, args...)
	cmd.Dir = r.cfg.Dir
	cmd.Env = r.env()

	var out bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &out

	err := cmd.Run()
	if err == nil {
		return nil
	}
	if ctx.Err() != nil {
		err = ctx.Err()
	}

	return fmt.Errorf("git %s: %w: %s", args[0], err, r.clean(out.String()))
}

// env is the whole environment of git: no inherited settings, no prompts,
// the token as a header.
func (r *Repo) env() []string {
	env := []string{
		"PATH=/usr/local/bin:/usr/bin:/bin",
		"HOME=" + r.cfg.Dir,
		"LANG=C.UTF-8",
		"GIT_TERMINAL_PROMPT=0",
		"GIT_CONFIG_NOSYSTEM=1",
		"GIT_CONFIG_GLOBAL=" + os.DevNull,
	}
	if r.cfg.Token == "" {
		return env
	}

	return append(env,
		"GIT_CONFIG_COUNT=1",
		"GIT_CONFIG_KEY_0=http.extraHeader",
		"GIT_CONFIG_VALUE_0=Authorization: token "+r.cfg.Token,
	)
}

// clean makes git's output fit for an error: no token, one short line.
func (r *Repo) clean(text string) string {
	if r.cfg.Token != "" {
		text = strings.ReplaceAll(text, r.cfg.Token, "***")
	}
	text = strings.Join(strings.Fields(text), " ")
	if len(text) > maxErrText {
		text = text[:maxErrText]
	}

	return text
}

// openModes lets the job users read the checkout: the server's umask is
// 077. Only the server writes. .git stays private; jobs do not need it.
func openModes(root string) error {
	return filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() && d.Name() == ".git" && p != root {
			return filepath.SkipDir
		}
		if d.Type()&fs.ModeSymlink != 0 {
			return nil
		}

		mode := os.FileMode(fileMode)
		if d.IsDir() {
			mode = dirMode
		}
		if info, err := d.Info(); err == nil && !d.IsDir() && info.Mode()&0o100 != 0 {
			mode = exeMode
		}

		return os.Chmod(p, mode)
	})
}
