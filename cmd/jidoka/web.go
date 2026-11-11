package main

import (
	"context"
	"database/sql"
	"errors"
	"net/http"
	"os"
	"time"

	"jidoka/internal/db"
	"jidoka/internal/model"
	"jidoka/internal/services/access"
	"jidoka/internal/web"
)

const (
	// tickStale: /healthz fails when the planner has not ticked for this
	// long (B5).
	tickStale         = time.Minute
	readHeaderTimeout = 10 * time.Second
	webStopWait       = 5 * time.Second
)

var errTickStale = errors.New("planner has not ticked for a minute")

// version is set at build time (-ldflags -X main.version=…).
var version = "dev"

// startWeb starts the web server; its error arrives on the channel. The
// server stops when ctx ends.
func (a *app) startWeb(ctx context.Context) <-chan error {
	acc := access.New(a.db, access.ConfigFromEnv(os.Getenv))
	srv := web.New(web.Deps{
		Access:   acc,
		Settings: func() model.Settings { return a.jobs.Current().Settings },
		Health:   a.health,
		Version:  version,
	})

	hs := &http.Server{Addr: a.listen, Handler: srv.Handler(), ReadHeaderTimeout: readHeaderTimeout}
	errc := make(chan error, 1)

	go func() {
		err := hs.ListenAndServe()
		if !errors.Is(err, http.ErrServerClosed) {
			errc <- err
		}
	}()

	go func() {
		<-ctx.Done()
		stopCtx, cancel := context.WithTimeout(context.Background(), webStopWait)
		defer cancel()
		_ = hs.Shutdown(stopCtx)
	}()

	return errc
}

// health is /healthz (B5): the planner ticked in the last minute and the
// database takes writes (an empty write transaction takes the lock at
// once, _txlock=immediate).
func (a *app) health() error {
	if time.Since(a.plan.LastTick()) > tickStale {
		return errTickStale
	}

	return db.WithTx(a.db, func(*sql.Tx) error { return nil })
}
