# Jidoka – Projekt-Wissen

Regeln für alle Repos: `RULES.md`. Hier nur das Besondere.

## Zweck
Kleine Skripte nach Zeitplan ausführen, Läufe anzeigen, bei Fehler,
Erfolg oder Ausbleiben melden (Apprise, E-Mail). Geschwister von Andon
(`../andon`): gleiche Technik, gleiche Gestaltung (Kante), enge
Integration. Anforderungen: `LASTENHEFT.md`.

## Grundsätze
- Jobs gehören nicht zu Jidoka: Ordner im privaten Repo `jidoka-jobs`, von Jidoka nach `/data/jobs` geholt (`JOBS_REPO`; ohne: Ordner `/jobs`), beim Start und bei jedem Neuladen.
  Die Oberfläche bearbeitet keine Jobs.
- Jidoka meldet selbst; es braucht Andon nicht, Andon braucht Jidoka nicht.
- Geheimnisse nie in Log, Ausgabe, Seite oder API.
- Uhrzeiten mit Zone, wo der Browser abweicht (wie Andon, `tzMark`).

## Schichten (nur zum direkten Nachbarn darunter)
```
cmd/jidoka      serve | validate | run | rotate-key | healthcheck
  ↓
web/            Routen, Templates, API, Ping, OIDC-Rückruf, CSRF   (net/http, html/template, htmx)
  ↓
services/       jobs (Laden, Neuladen)   plan (Planer, Schlange, Sperren)   runs (Lauf ausführen)
                watch (Wächter)   alerts (Meldungen)   secrets   access (Anmeldung, Token)   status
  ↓
repos/          SQL je Tabelle        drivers/  proc (Prozess), apprise, smtp
  ↓
db/             database/sql, Migrationen
```
Reine Pakete ohne I/O, von jeder Schicht nutzbar: `model` (Typen), `cron`
(Zeitplan mit Zone), `conf` (TOML → `model.Set`), `redact` (Geheimnisse
maskieren), `crypto`, `i18n`.

- Routen rufen nie Repos oder Drivers direkt auf.
- Ablauf: `jobs` liefert ein `model.Set`; `plan` fragt je Takt (1 s) nach
  fälligen Terminen und reicht sie an `runs`; `runs` startet über `proc`,
  speichert den Lauf und gibt das Ergebnis an `watch` und `alerts`.
- Zeiten in der Datenbank als UTC-Text (`db.TimeStr`), gelesen mit `db.ParseTime`.
- Die Datenbank ist nicht als Datei verschlüsselt, nur Spalten `*_enc`:
  Jidoka startet auch ohne `MASTER_KEY` (Z7).
- Übernommener Code aus Andon nennt im Dateikopf Pfad und Commit in Andon (I10).
- `make check` vor jedem Commit (`GOTOOLCHAIN=go1.26.10`, sonst bricht staticcheck).
