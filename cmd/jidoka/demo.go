//go:build !release

package main

import (
	"fmt"
	"log/slog"
	"os"
	"time"

	"jidoka/internal/demo"
)

const (
	demoName    = "demo"
	demoDefData = "./data-demo"
	// demoKey seals the demo's dummy secrets; never a real key.
	demoKey      = "demo"
	demoListen   = "127.0.0.1:8080"
	envToday     = "DEMO_TODAY"
	envDev       = "JIDOKA_DEV"
	todayLayout  = "2006-01-02"
	langDE       = "de"
	langEN       = "en"
	defaultLang  = langDE
	usageDemo    = "usage: jidoka demo [de|en]"
	envTrueValue = "true"
)

func init() {
	extraCommands[demoName] = runDemo
}

// runDemo seeds the demo (jobs folder and database), then serves it
// with the dev login. It listens on localhost only, because the dev
// login has no password.
func runDemo(args []string) int {
	lang := defaultLang
	if len(args) > 1 || (len(args) == 1 && args[0] != langDE && args[0] != langEN) {
		fmt.Fprintln(os.Stderr, usageDemo)
		return exitUsage
	}
	if len(args) == 1 {
		lang = args[0]
	}

	now, err := demoNow(time.Now(), os.Getenv(envToday))
	if err != nil {
		fmt.Fprintf(os.Stderr, "jidoka: %s: %v\n", envToday, err)
		return exitUsage
	}

	dataDir := envOr(envData, demoDefData)
	os.Setenv(envKey, demoKey)

	dir, err := demo.Prepare(dataDir, lang, now)
	if err != nil {
		slog.Error("demo failed", "err", err)
		return exitInvalid
	}

	os.Setenv(envData, dataDir)
	os.Setenv(envJobs, dir)
	os.Setenv(envDev, envTrueValue)
	if os.Getenv(envListen) == "" {
		os.Setenv(envListen, demoListen)
	}

	return serve()
}

// demoNow is the demo's clock: DEMO_TODAY fixes the day, the time of day
// stays the real one.
func demoNow(real time.Time, today string) (time.Time, error) {
	if today == "" {
		return real, nil
	}

	day, err := time.ParseInLocation(todayLayout, today, real.Location())
	if err != nil {
		return real, err
	}

	return time.Date(day.Year(), day.Month(), day.Day(), real.Hour(), real.Minute(), real.Second(), 0, real.Location()), nil
}
