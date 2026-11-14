# Architecture

How this repository is meant to be built. Where the code differs today is listed under
"Known deviations". Change this file in the PR that changes the structure.

## What it does

Jidoka runs jobs on a schedule on a server (a cron with a web interface): it runs each
job, keeps the result, watches for missed runs and pings, and reports failures by
Apprise, mail and to Andon. The jobs come from a private Git repository; the web
interface shows them and never edits them.

## Building blocks

All under `internal/` unless the path starts elsewhere.

| Path | Its one task |
|---|---|
| `cmd/jidoka/` | The program: `serve`, `validate`, `run`, `rotate-key`, `healthcheck`. `serve` opens the database and wires the parts; it decides nothing. |
| `cmd/jidoka-shim/` | The small program that starts a job's process. |
| `web/` | Routes, templates, API, ping, sign-in callback, CSRF. |
| `services/jobs`, `plan`, `runs` | Loading and reloading the jobs; the planner with queue and locks; running one job. |
| `services/watch`, `alerts` | The watchers (missed run, missed ping); who is told what, when. |
| `services/status`, `ops` | What the pages show (overview, job page, secrets page); the owner's actions (acknowledge, mute, reload, cancel a run). |
| `services/settings`, `secrets`, `access` | The global settings; secrets of jobs and of Jidoka itself; sign-in, sessions, tokens. |
| `services/beat`, `andon` | The heartbeat to a monitoring service; the state of each job for Andon. |
| `repos/` | The SQL, one file per table. |
| `drivers/` | The outside world, one package each: `proc` (processes), `git` (the jobs repository), `apprise`, `smtp`, `oidc`. |
| `db/` | Opening the database, migrations, transactions, times as text. |
| `model`, `cron`, `conf`, `redact`, `crypto`, `i18n` | Pure packages without I/O that every layer may use: the types; the schedule with time zone; job folder and settings to `model.Set`; masking secrets; encryption; texts. |
| `demo/` | The demo world, only in a build without the tag `release`. |
| `deploy/`, `Dockerfile`, `entrypoint.sh`, `scripts/`, `testdata/`, `tools/` | The container and an example for running it; the release check and image test; the sync of the design system and the structure check (synced copy). |

## Layers

Each layer talks only to its neighbour directly below.

```
 cmd/jidoka      serve wires the parts
      │
      ▼
 web/            routes, templates, API
      │
      ▼
 services/       jobs ─► plan ─► runs ─► watch, alerts     status, ops, settings,
      │                                                    secrets, access, beat, andon
      ├──────────────────────┐
      ▼                      ▼
 repos/  (SQL per table)   drivers/  proc, git, apprise, smtp, oidc ─► processes, network
      │
      ▼
 db/             database/sql, migrations

 model, cron, conf, redact, crypto, i18n: pure, used by every layer
```

- A route never calls a repository or a driver; it asks a service.
- A service decides; a repository only stores and reads; a driver only talks to the
  outside. Network and processes are reached through `drivers/` alone.
- One exception to the neighbour rule: a service opens its transaction itself
  through `db/` and hands it to the repositories, so that one decision is written
  whole or not at all.
- The flow: `jobs` delivers a `model.Set`; `plan` asks every second for what is due
  and hands it to `runs`; `runs` starts the process through `proc`, stores the run and
  gives the result to `watch` and `alerts`. `serve` connects them and adds no rule.
- A rule about jobs stands once, beside its type in `model` or `conf`: which alert
  rules apply to a job, which jobs are known, which states count as bad. Services,
  pages and the demo ask it.
- A default stands once, in `conf`.
- Jidoka's own secrets are listed once, in `services/secrets`; masking a secret is
  built once.
- A function takes only what it uses.
- Jidoka and Andon do not need each other; `services/andon` only reports.
- Demo code is in no release build; the release check proves it.

## Where new things go

| Change | Touch |
|---|---|
| A new field of a job | `conf` (parsing, validation), `model`; the service that uses it; the job page in `services/status` and its template |
| A new page or API address | A route in `web/` and its template; what it shows from `services/status` or the service of its topic |
| A new action of the owner | `services/ops`, a route in `web/routes_actions.go` |
| A new way to report | A driver in `drivers/`, its use in `services/alerts`; the channel in `conf` and on the settings page |
| A new watcher | `services/watch` |
| A new global setting | `conf/settings.go` (name and default), `services/settings`, the settings page |
| A new table or column | A migration in `db/migrations`, its file in `repos/` |
| A new secret of Jidoka itself | `services/secrets` |
| A new command | `cmd/jidoka/` |
| A design element that is missing | Kante, then `tools/sync-design.sh`; never `web/static/vendor/` here |

## Known deviations

- Which alert rules apply to a job is built three times: alerts, job page, demo
  (`AUDIT.md`, finding 1).
- Defaults stand twice: planner, demo and three packages repeat what `conf` has
  (`AUDIT.md`, finding 2).
- The function for a finished run takes two arguments that change nothing
  (`AUDIT.md`, finding 3).
- "Which jobs are known" is answered in four places (`AUDIT.md`, finding 4).
- `serve` decides which open incidents close after a reload, maps a run's end to
  the state for Andon and repeats fetching the jobs repository
  (`AUDIT.md`, finding 5).
- Jidoka's own secrets are listed in three places; masking is built twice
  (`AUDIT.md`, finding 6).
- `services/beat` and `services/andon` send their requests themselves, not through a
  driver (`AUDIT.md`, finding 7).
- The group of bad states is listed by hand in three places (`AUDIT.md`, finding 9).
