package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func write(t *testing.T, files map[string]string) string {
	t.Helper()

	dir := t.TempDir()
	for name, content := range files {
		p := filepath.Join(dir, name)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	return dir
}

func TestValidateOK(t *testing.T) {
	dir := write(t, map[string]string{
		"a/job.toml":   "run = \"x.sh\"\nschedule = \"0 6 * * *\"\n",
		"a/x.sh":       "",
		"w/watch.toml": "period = \"24h\"\ngrace = \"1h\"\n",
	})

	var out, errOut bytes.Buffer
	if code := validate(dir, &out, &errOut); code != exitOK {
		t.Fatalf("exit %d: %s%s", code, out.String(), errOut.String())
	}

	got := out.String()
	if strings.Count(got, "06:00") != nextSlots || !strings.Contains(got, "2 valid, 0 invalid") ||
		!strings.Contains(got, "every 24h0m0s") {
		t.Errorf("output:\n%s", got)
	}
	if errOut.Len() != 0 {
		t.Errorf("stderr: %s", errOut.String())
	}
}

func TestValidateErrors(t *testing.T) {
	dir := write(t, map[string]string{
		"jidoka.toml": "zone = \"Mars/Base\"\n",
		"a/job.toml":  "run = \"x.sh\"\nschedule = \"0 25 * * *\"\n",
		"a/x.sh":      "",
	})

	var out, errOut bytes.Buffer
	if code := validate(dir, &out, &errOut); code != exitInvalid {
		t.Fatalf("exit %d", code)
	}

	got := errOut.String()
	for _, want := range []string{"jidoka.toml:1: unknown zone", "a/job.toml:2: invalid schedule: hour"} {
		if !strings.Contains(got, want) {
			t.Errorf("stderr missing %q:\n%s", want, got)
		}
	}
}

func TestRun(t *testing.T) {
	if code := run(nil); code != exitUsage {
		t.Errorf("no args: %d", code)
	}
	if code := run([]string{"nope"}); code != exitUsage {
		t.Errorf("unknown command: %d", code)
	}
	if code := run([]string{"validate"}); code != exitUsage {
		t.Errorf("validate without dir: %d", code)
	}
}
