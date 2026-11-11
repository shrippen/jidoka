package main

import (
	"bufio"
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"jidoka/internal/conf"
	"jidoka/internal/crypto"
	"jidoka/internal/db"
	"jidoka/internal/model"
	"jidoka/internal/services/runs"
	"jidoka/internal/services/secrets"
)

const (
	healthTimeout = 5 * time.Second
	envNewKey     = "NEW_MASTER_KEY"
	localActor    = "local"
)

// healthcheck asks the running server's /healthz (Docker HEALTHCHECK).
func healthcheck() int {
	addr := envOr(envListen, defListen)
	if strings.HasPrefix(addr, ":") {
		addr = "127.0.0.1" + addr
	}

	client := http.Client{Timeout: healthTimeout}
	resp, err := client.Get("http://" + addr + "/healthz")
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return exitInvalid
	}
	resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		fmt.Fprintln(os.Stderr, "unhealthy:", resp.Status)
		return exitInvalid
	}
	return exitOK
}

// rotateKey re-encrypts every secret and ping token under NEW_MASTER_KEY
// (Z7). The salt stays; afterwards Komodo must hand out the new key.
// Run it with the server stopped.
func rotateKey() int {
	dataDir := envOr(envData, defData)
	if err := initKey(dataDir); err != nil || !crypto.Ready() {
		fmt.Fprintln(os.Stderr, "MASTER_KEY (the current key) is needed")
		return exitInvalid
	}

	next := os.Getenv(envNewKey)
	if next == "" {
		fmt.Fprintln(os.Stderr, envNewKey+" is needed")
		return exitUsage
	}

	salt, err := db.ReadSalt(filepath.Join(dataDir, saltFile))
	if err != nil {
		fmt.Fprintln(os.Stderr, "salt:", err)
		return exitInvalid
	}

	d, err := db.Open(filepath.Join(dataDir, dbFile))
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return exitInvalid
	}
	defer d.Close()

	if err := secrets.New(d).Rotate(crypto.Derive(next, salt)); err != nil {
		fmt.Fprintln(os.Stderr, err)
		return exitInvalid
	}

	fmt.Println("done: set MASTER_KEY to the new key in Komodo and Vaultwarden")
	return exitOK
}

// runLocal runs one job once, outside the server, with secrets from a
// local KEY=VALUE file (T2): jidoka run [-secrets f] [-dry] <jobs> <id>.
// It uses a throwaway database, so nothing is kept.
func runLocal(args []string) int {
	fs := flag.NewFlagSet("run", flag.ContinueOnError)
	secretFile := fs.String("secrets", "", "KEY=VALUE file, not checked in")
	dry := fs.Bool("dry", false, "dry run (JIDOKA_TRIGGER=dry)")
	if err := fs.Parse(args); err != nil || fs.NArg() != 2 {
		fmt.Fprintln(os.Stderr, "usage: jidoka run [-secrets file] [-dry] <jobs-dir> <id>")
		return exitUsage
	}

	set := conf.Load(fs.Arg(0), nil)
	job, ok := set.Jobs[fs.Arg(1)]
	if !ok || job.Kind != model.KindJob {
		fmt.Fprintf(os.Stderr, "no valid job %q\n", fs.Arg(1))
		printErrors(os.Stderr, set)
		return exitInvalid
	}

	tmp, err := os.MkdirTemp("", "jidoka-run-")
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return exitInvalid
	}
	defer os.RemoveAll(tmp)

	run, err := execLocal(tmp, job, *secretFile, *dry, set.Commit)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return exitInvalid
	}

	fmt.Print(run.Output)
	fmt.Fprintf(os.Stderr, "\n%s in %s", run.State, run.Duration(time.Now()).Round(time.Millisecond))
	if run.Summary != "" {
		fmt.Fprintf(os.Stderr, " · %s", run.Summary)
	}
	if run.Error != "" {
		fmt.Fprintf(os.Stderr, " · %s", run.Error)
	}
	fmt.Fprintln(os.Stderr)

	if run.State != model.RunOK {
		return exitInvalid
	}
	return exitOK
}

// execLocal stores the file's secrets under a random key in a throwaway
// database and runs the job through the same runs service as the server.
func execLocal(dir string, job *model.Job, secretFile string, dry bool, commit string) (model.Run, error) {
	crypto.Init(crypto.Derive(crypto.NewToken(), crypto.NewSalt()))

	d, err := db.Open(filepath.Join(dir, dbFile))
	if err != nil {
		return model.Run{}, err
	}
	defer d.Close()

	sec := secrets.New(d)
	if err := loadSecrets(sec, secretFile); err != nil {
		return model.Run{}, err
	}

	trig := model.TrigManual
	if dry {
		trig = model.TrigDry
	}

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGTERM, syscall.SIGINT)
	defer stop()

	svc := runs.New(d, sec, runs.Config{DataDir: dir, Shim: os.Getenv(envShim)})
	return svc.Execute(ctx, job, trig, time.Now(), 1, commit), nil
}

// loadSecrets reads KEY=VALUE lines; blank lines and # comments are skipped.
func loadSecrets(sec *secrets.Service, path string) error {
	if path == "" {
		return nil
	}

	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()

	return eachLine(f, func(line string) error {
		name, value, ok := strings.Cut(line, "=")
		if !ok {
			return errors.New("secrets file: line without '='")
		}
		return sec.Set(strings.TrimSpace(name), value, localActor)
	})
}

func eachLine(r io.Reader, fn func(string) error) error {
	sc := bufio.NewScanner(r)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		if err := fn(line); err != nil {
			return err
		}
	}
	return sc.Err()
}

// printErrors lists load errors as file:line: msg.
func printErrors(w io.Writer, set model.Set) {
	for _, le := range set.Invalid {
		fmt.Fprintf(w, "%s:%d: %s\n", le.File, le.Line, le.Msg)
	}
}
