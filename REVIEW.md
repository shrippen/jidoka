# Review guide

Every PR is reviewed before it is merged (`RULES.md`, repository rule).
The reviewer gets the PR's intent, its diff, the repository and this
file. The owner does not read code; this review is the second reader.

## What failure looks like here

Jidoka runs the owner's small scripts on a schedule and reports on them.
A bug that matters is one that hides a failure or causes harm:

- **Silent failure:** a failed, timed-out or missing run that sends no
  message or shows as fine; a message for the wrong job or state.
- **Noise:** repeated messages for one ongoing failure, messages after
  a restart for states already reported.
- **Wrong runs:** a job that runs twice at once, at the wrong time or
  zone, after it was removed or paused, or not at all after a restart.
- **Leaks:** secrets in a log, run output, page, API answer or message;
  a page or API route reachable without login or token.
- **Stops the service:** a panic, a hung job blocking others, an invalid
  job file that stops the start.
- **Lies:** docs or comments the code contradicts.

## What to check in particular

- Process handling: timeouts kill the whole process group; output size
  is capped; exit codes map to the documented states.
- Times stored in UTC, shown with zone; cron in the job's zone, also
  across daylight-saving changes.
- GUI rule: Kante as it is, no hex or `px` outside the vendored Kante.
- Bug fixes: a test that failed before the fix.

## Skip

- Vendored Kante files, `go.sum`, images; style that `gofmt`, `go vet`
  and `staticcheck` enforce.

## How to report

- Every finding names file and line and the concrete input or state that
  goes wrong. No guess without evidence; if unsure, say so.
- Each finding gets one label: **Wichtig** (a bug from the list above,
  must be fixed or answered before the merge), **Kleinigkeit** (optional,
  at most five) or **Bestand** (the bug was there before this PR).
- The report starts with the count, e.g. "2 Wichtig, 1 Kleinigkeit".
  Only when none is open it says plainly: "Nichts zu beheben."
- Findings in German, code names as they are.
