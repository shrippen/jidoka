# Lastenheft Jidoka

Stand: 11.11.2026, Entwurf, überarbeitet nach Review (Fable-Agent).
Entscheidungen des Eigentümers: Abschnitt 9.

## 1. Zweck

Jidoka führt kleine Skripte nach Zeitplan aus, zeigt ihren Stand auf
einer Webseite und meldet über Apprise und E-Mail, wenn ein Lauf
scheitert, gelingt oder ausbleibt.

Der Name stammt aus dem Toyota-Produktionssystem (自働化): Automation,
die bei einer Störung anhält und meldet. Andon ist dort das Werkzeug von
Jidoka. Darum stehen beide nebeneinander: Jidoka führt aus und meldet,
Andon zeigt den Zustand aller Dienste, Jidoka eingeschlossen.

Erster Job: die Lehrgänge von shinkiryu.de als Termine in einen
Davis-Kalender schreiben.

## 2. Überblick

```
 ploetze (neben Andon)                        eredin
 ┌──────────────────── Container jidoka ──────────────┐   ┌────────────────┐
 │ /data/jobs (Git)     Planer ─► Läufer ─► Skript     │   │ Apprise, SMTP  │
 │  jidoka.toml ─Laden─►  │        │  (eigene UID)    │──►│ Uptime Kuma ◄──┼─ Herzschlag
 │  shinkiryu/            │        ▼                  │   │ Komodo         │
 │   job.toml, main.py    │     Läufe (SQLite, /data) │   └────────────────┘
 │ Geheimnisse (verschl.) ▼        │  MASTER_KEY ◄ Komodo
 │                     Wächter ────┤ Zustandswechsel  │
 │                                 ▼                  │
 │                              Melder ──► Apprise, SMTP
 │ Web (Kante, htmx)  ◄── Tafel, Fahrplan, Job, Lauf  │
 │ API (Token)        ◄── Andon, Komodo (Neuladen)    │
 │ /ping/<token>      ◄── externe Wächter (Borg …)    │
 └────────────────────────────────────────────────────┘
       ▲ Anmeldung: Authentik (OIDC), Zugriff regelt Authentik
```

## 3. Begriffe

| Begriff | Bedeutung |
|---|---|
| Job | Ein Ordner im Jobs-Repo (J1) mit `job.toml` und einem ausführbaren Skript. Seine **Id** ist der Ordnername (`[a-z0-9-]+`); `name` ist nur Anzeige. Ein umbenannter Ordner ist ein neuer Job. |
| Wächter | Externer Job ohne Skript (`watch.toml`): Jidoka erwartet Pings in einem Zeitfenster. |
| Lauf | Eine Ausführung eines Jobs oder ein Ping eines Wächters. |
| Lauf-Zustand | `läuft`, `ok`, `fehler`, `zeitüberschreitung`, `abgebrochen` (Container-Stopp, Absturz), `entfallen` (Termin fiel in einen laufenden Lauf). |
| Job-Zustand | Ergebnis des letzten Laufs, oder: `nie gelaufen`, `ausgeblieben` (Wächterfenster überschritten), `pausiert` (Oberfläche), `deaktiviert` (`enabled = false`), `ungültig` (nicht geladen). |
| Kanal | Ziel einer Meldung: Apprise (mit Tags als Unterkanälen) oder E-Mail. |
| Auslöser | Ereignis, das eine Meldungsregel auslöst (M2). |

## 4. Anforderungen

Priorität: **M** muss, **S** soll, **K** kann.

### 4.1 Jobs

| Nr. | Prio | Anforderung |
|---|---|---|
| J1 | M | Jobs sind Ordner eines privaten Git-Repos auf Gitea (`jidoka-jobs`). Jidoka holt es selbst, beim Start und bei jedem Neuladen (J20): Adresse, Branch und Zugangs-Token kommen als Umgebungsvariablen (`JOBS_REPO`, `JOBS_BRANCH`, `JOBS_REPO_TOKEN`). Der Checkout liegt in `/data/jobs`; ohne `JOBS_REPO` liest Jidoka einen eingebundenen Ordner `/jobs` (Entwicklung). Der Token liegt nie im Checkout (nicht in `.git/config`, nicht in der URL) und nirgends, wo Jobs lesen können. Scheitert das Holen (Gitea nicht erreichbar, Token abgelaufen), gilt der zuletzt geholte Stand in `/data/jobs` weiter, der Fehler steht oben (O9) und geht als Meldung raus (Profil `standard`); beim nächsten Neuladen und, solange es scheitert, alle 5 Minuten wird es erneut versucht. Beim allerersten Start ohne geholten Stand startet Jidoka ohne Jobs und ohne Kanäle (B8); der Fehler steht oben, eine Meldung ist dann nicht möglich, das Ausbleiben des Herzschlags fällt in Uptime Kuma auf. |
| J2 | M | Ein fehlerhafter Job verhindert weder Start noch Neuladen. Er wird `ungültig`, mit Fehlertext auf der Seite und Meldung (`invalid`). Eine ältere gültige Fassung läuft nicht weiter, damit Laufzeit und Neustart gleich ausgehen. |
| J3 | M | Zeitplan als Cron-Ausdruck (5 Felder) in der Zone des Jobs, Standard `Europe/Berlin`. Sommerzeit: Ein Termin in der fehlenden Stunde (02:30 im März) läuft um 03:00; in der doppelten Stunde im Oktober läuft er nur beim ersten Mal. Die Zonendaten liegen im Binary. |
| J4 | M | Erfolg heißt Exit-Code 0, alles andere `fehler`. Beim Timeout (Standard 10 min) bekommt die Prozessgruppe SIGTERM, nach 10 s SIGKILL; Zustand `zeitüberschreitung`. Ein Shim je Lauf ist Subreaper und beendet auch Kinder, die sich lösen (`setsid`, Double-Fork); ein Init-Prozess im Container sammelt Zombies ein. |
| J5 | M | Ein Job läuft nie zweimal gleichzeitig. Fällt ein Termin in einen laufenden Lauf, wird er als `entfallen` vermerkt. |
| J6 | M | Ausgabe (stdout und stderr, zeitlich verschränkt) wird je Lauf gespeichert, gekürzt auf 256 KB (Anfang und Ende bleiben). |
| J7 | M | Geheimnisse liegen verschlüsselt in Jidokas Datenbank (Z7) und werden auf der Seite „Geheimnisse“ gesetzt (O11). Ein Job nennt in `job.toml` die Namen, die er braucht (`secrets = ["DAVIS_PASSWORD"]`); nur diese bekommt er als Umgebungsvariablen. Fehlt ein genanntes Geheimnis, scheitert der Lauf mit klarer Meldung, ohne das Skript zu starten. Werte ab 6 Zeichen werden in Ausgabe, Seite, API und Meldung durch `***` ersetzt, auch in Base64- und URL-Kodierung. Das ist Schadensbegrenzung, keine Sicherheitsgrenze. |
| J8 | S | Ein Skript kann eine Zeile Zusammenfassung in `$JIDOKA_SUMMARY` schreiben, z. B. „11 Lehrgänge, 2 neu, 0 entfernt“. Sie steht in Übersicht und Meldung. |
| J9 | S | Jobs von Hand starten, pausieren und einen laufenden Lauf abbrechen, über die Seite. Pause überlebt den Neustart. |
| J10 | S | `catchup = true`: Ein Termin, der verpasst wurde, weil der Container aus war, wird nach dem Start einmal nachgeholt. |
| J11 | S | Laufzeit im Image: `sh`, `bash`, `curl`, `jq`, Python 3 mit `uv`. Abhängigkeiten im Skriptkopf (PEP 723) mit Lockdatei je Skript (`uv lock --script`); `uv` lädt kein Python nach. Paket-Cache je Job (Z3: kein Job schreibt in den Cache eines anderen), zusammen auf 2 GB begrenzt: stündlich werden ganze Caches gelöscht, der des am längsten nicht gelaufenen Jobs zuerst, nie der eines laufenden. |
| J12 | S | Aufbewahrung: Läufe 180 Tage, höchstens 500 je Job; danach gelöscht. |
| J13 | S | Wiederholung nach Fehler (`retries`, `retry_delay`). Ein Termin mit allen Wiederholungen zählt als **ein** Ergebnis; gemeldet wird nach dem letzten Versuch. |
| J14 | S | `jitter`: zufällige Verzögerung bis zur angegebenen Dauer, damit Jobs fremde Server nicht immer zur selben Sekunde treffen. |
| J15 | S | `lock`: Jobs mit gleichem Lock laufen nie gleichzeitig (z. B. zwei Jobs, die in Davis schreiben). |
| J16 | S | `args` und `[env]`: Argumente und Umgebungsvariablen ohne Geheimnis. Die Umgebung des Jobs ist bereinigt: nur `PATH`, `HOME`, `TZ`, `LANG`, `TMPDIR` (J22), die Variablen der Laufzeit für `uv` (J11), `[env]`, die genannten Geheimnisse und die `JIDOKA_*`-Variablen. Jidoka setzt `JIDOKA_JOB`, `JIDOKA_RUN`, `JIDOKA_TRIGGER` (Zeitplan, Hand, Nachholen, Wiederholung, Probelauf), `JIDOKA_LAST_OK`, `JIDOKA_STATE`, `JIDOKA_SUMMARY`, `JIDOKA_NOTIFY`. |
| J17 | S | `description`, `link`, `tags`: Beschreibung und Ziel-Link auf der Job-Seite, Filter nach Tags auf der Tafel. |
| J18 | S | `keep` je Job: Aufbewahrung abweichend vom Standard. |
| J19 | S | `enabled = false`: Job bleibt sichtbar als `deaktiviert`, läuft nicht, sein Wächter ruht. |
| J20 | M | Neuladen zur Laufzeit, ohne Neustart: Knopf „Jobs neu laden“ und `POST /api/v1/reload` (Token mit Recht `reload`). Jidoka liest alle Jobs, prüft sie und tauscht erst dann aus; laufende Läufe enden mit der alten Fassung. Das Ergebnis steht auf der Seite: neu, geändert, entfernt, ungültig, dazu der geholte Commit. Ein Neuladen, das Jobs ändert, löst eine Meldung aus (Auslöser `reload`). |
| J21 | M | Ein entfernter Job verschwindet von der Tafel; seine Läufe bleiben bis zum Ende der Aufbewahrung unter „Entfernt“ lesbar. Pause, Bestätigen und Stummschalten hängen an der Id und gelten wieder, wenn der Job zurückkommt. |
| J22 | M | Zustandsordner je Job (`$JIDOKA_STATE` unter `/data/state/<id>`), bleibt über Läufe erhalten, z. B. für ETag oder Inhalts-Hash. Dazu ein leeres `$TMPDIR` je Lauf, danach gelöscht. |
| J23 | M | Absturz und Stopp: Bei SIGTERM an den Container beendet Jidoka laufende Jobs wie J4 und vermerkt `abgebrochen`. Läufe, die beim Start noch `läuft` heißen, werden `abgebrochen`. Beides meldet keinen Fehler. Bei `catchup = true` wird ein so abgebrochener Termin nach dem Start einmal nachgeholt (wie J10). |
| J24 | S | Probelauf per Knopf: Lauf mit `JIDOKA_TRIGGER=dry`; das Skript soll dann nur zeigen, was es täte. Probeläufe zählen nicht für Zustand und Wächter. |

Beispiel `job.toml` (alle Felder außer `run` und `schedule` sind
optional):

```toml
name        = "Shinkiryu-Lehrgänge → Kalender"
description = "Lehrgänge von shinkiryu.de in den Davis-Kalender Aikido"
link        = "http://cal.arianw.de/dav/calendars/shinki/Lehrgaenge/"  # Ziel, auf der Job-Seite verlinkt
tags        = ["aikido", "kalender"]
run         = "main.py"
schedule    = "0 6 * * *"
zone        = "Europe/Berlin"
jitter      = "5m"        # zufällige Verzögerung, schont fremde Server
timeout     = "5m"
slow        = "2m"        # Auslöser slow ab 2 min
retries     = 2           # bei Fehler noch zweimal versuchen …
retry_delay = "10m"       # … im Abstand von 10 min
catchup     = true
lock        = "davis"     # Jobs mit gleichem lock laufen nie gleichzeitig
expect      = "26h"       # Wächter: ein Erfolg je 26 h
keep        = "365d"      # Aufbewahrung der Läufe, abweichend vom Standard
quiet       = "22:00-07:00"

secrets     = ["DAVIS_USER", "DAVIS_PASSWORD"]  # Namen; Werte nur in Jidoka

[env]                     # Werte ohne Geheimnis
DAVIS_URL = "http://cal.arianw.de/dav/calendars/shinki/Lehrgaenge/"

[[notify]]                # Störungen aufs Handy und per Mail
on       = ["fail", "timeout", "missed"]
after    = 2              # erst ab dem 2. Termin in Folge mit Störung
channels = ["apprise:handy", "mail"]
remind   = "24h"
urgent   = true           # durchbricht die Ruhezeit

[[notify]]                # Neue Lehrgänge: Nachricht des Skripts
on       = ["change"]
channels = ["apprise:handy"]

[[notify]]                # Erholung und Erfolge gesammelt per Mail
on       = ["recover", "success"]
channels = ["mail"]
to       = ["arian@…"]
digest   = "weekly Mon 08:00"
tail     = 0              # keine Ausgabe in der Meldung
```

Ohne `[[notify]]` gilt das Profil `standard` aus `jidoka.toml`. Ein Job
kann auch `notify_profile = "leise"` setzen:

```toml
# jidoka.toml im Jobs-Repo (kein Geheimnis hier; Passwörter auf der Seite „Geheimnisse“)
zone  = "Europe/Berlin"
locale = "de"             # Sprache der Meldungen: de oder en
quiet = "22:00-07:00"     # Ruhezeit in dieser Zone
heartbeat = "5m"          # eigener Herzschlag an Uptime Kuma (URL als Geheimnis)

[channels.apprise]
url  = "http://apprise-eredin:8000/notify"   # Schlüssel: Geheimnis APPRISE_KEY
tags = { handy = "handy", desktop = "kde" }   # apprise:handy → Apprise-Tag "handy"

[channels.mail]
smtp = "smtp.eredin:587"
from = "jidoka@arianw.de"
to   = ["arian@…"]

[[profiles.standard]]
on = ["fail", "timeout", "missed", "invalid", "reload"]
channels = ["apprise", "mail"]
remind = "24h"

[[profiles.standard]]
on = ["recover"]
channels = ["apprise"]

[[profiles.leise]]
on = ["fail", "missed"]
after = 3
channels = ["mail"]
```

### 4.2 Wächter (Totmannschalter)

| Nr. | Prio | Anforderung |
|---|---|---|
| W1 | M | `expect` je Job: Gab es im Fenster keinen erfolgreichen Lauf, wird der Job `ausgeblieben`. Das Fenster zählt ab dem letzten Erfolg, bei neuen Jobs ab dem ersten Laden. Es ruht bei Pause und `enabled = false`. Nach einem Start gilt eine Karenz von `timeout` + `jitter` + 5 min, damit J10 zuerst nachholen kann. Ein Exit 0 ohne Wirkung erkennt der Wächter nicht; dafür ist `$JIDOKA_SUMMARY` da. |
| W2 | S | Externe Wächter (z. B. Borg, Snapshots): Datei `<id>/watch.toml` im Jobs-Repo mit `period` oder `schedule` und `grace`. Ping über `POST /ping/<token>`, `/start`, `/fail` (Körper bis 10 KB wird zur Ausgabe). Erreichbar im LAN und über Tailscale. |
| W3 | M | Ping-Token stehen nicht im Git: Jidoka erzeugt sie in `/data` und zeigt sie auf der Wächter-Seite mit Kopierknopf; Erneuern per Knopf. Unbekannte Token bekommen 404; Drosselung je Adresse (Muster wie Andons `hooks`). |

### 4.3 Meldungen

| Nr. | Prio | Anforderung |
|---|---|---|
| M1 | M | Kanäle Apprise (Apprise-API auf eredin) und E-Mail (SMTP auf eredin), eingestellt in `jidoka.toml` im Jobs-Repo, Passwörter und Schlüssel als Geheimnisse (Z5). Apprise-Tags als Unterkanäle (`apprise:handy`), Mail mit eigenen Empfängern je Regel (`to`). Sprache der Meldungen per `locale` in `jidoka.toml` (`de`, `en`; Standard `de`). |
| M2 | M | Auslöser: `fail`, `timeout`, `missed`, `recover` (erster Erfolg nach Störung), `success`, `invalid` (Job nicht geladen), `reload` (Jobs geändert), `change` (Skript hat in `$JIDOKA_NOTIFY` geschrieben). **S:** `start`, `slow` (Lauf länger als `slow`). |
| M3 | M | Regeln je Job (`[[notify]]`): Auslöser, Kanäle, `after` (ab dem wievielten **Termin** in Folge mit Störung; Wiederholungen nach J13 zählen nicht extra), `remind` (oder `"never"`), `urgent` (durchbricht die Ruhezeit), `tail`. Der Apprise-Typ folgt aus dem Auslöser: Störung `failure`, `missed` `warning`, `recover`/`success` `success`, sonst `info`. Ohne Regeln gilt das Profil `standard`. |
| M4 | M | Profile in `jidoka.toml`: benannte Regelsätze, die Jobs mit `notify_profile` übernehmen. Regeln im Job ersetzen das Profil ganz, mischen nicht. Ereignisse ohne lesbare Job-Regeln (`reload`, `invalid` eines Jobs, dessen `job.toml` nicht lädt, eine kaputte `jidoka.toml`) folgen immer dem Profil `standard` (bei kaputter `jidoka.toml` dem der letzten gültigen Fassung, B8). |
| M5 | M | Keine Flut: Bei anhaltender Störung nur die erste Meldung, dann Erinnerungen nach `remind`. Ein Neustart wiederholt keine Meldung, die schon gesendet ist. |
| M6 | M | Ruhezeit (`quiet`) global in der Zone von `jidoka.toml`, je Job überschreibbar. Meldungen warten bis zum Ende und gehen dann gebündelt raus, mit dem Endstand: Störung und Erholung in derselben Ruhezeit ergeben eine Meldung „gestört 02:15, erholt 03:15“. `urgent` gilt sofort. |
| M7 | S | Sammelmeldung (`digest = "daily 08:00"` oder `"weekly Mon 08:00"`) nur für gute Nachrichten: `success` und `recover`. `change` und `reload` warten nie auf eine Sammelmeldung (Z6), nur auf das Ende der Ruhezeit (M6). Störungen gehen immer einzeln und sofort (Ruhezeit ausgenommen). |
| M8 | M | Inhalt: Job, Zustand, Zeit mit Zone, Dauer, Zusammenfassung, letzte Zeilen der Ausgabe (`tail`, Standard 20, `0` = keine), Link zum Lauf. Mail optional mit ganzer Ausgabe als Anhang (`attach_output = true`). |
| M9 | M | Versand wird je Kanal bis zu 3-mal versucht (sofort, nach 1 und nach 5 min). Danach gilt der Kanal als gestört: Fehler oben auf der Seite, und Meldungen mit diesem Kanal gehen über den anderen. Regeln mit nur einem Kanal nutzen ebenfalls den anderen. Scheitern beide, bleibt die Meldung in `/data`; sie wird alle 15 min neu versucht (ein Versuch je Kanal) und geht spätestens mit dem nächsten erfolgreichen Versand raus. Ein gestörter Kanal wird beim nächsten fälligen Versand erneut probiert, frühestens 5 min nach dem letzten Versuch, und gilt nach dem ersten Erfolg wieder als gesund. |
| M10 | S | In der Oberfläche, ohne das Repo zu ändern: Störung **bestätigen** (keine Erinnerungen mehr, bis der Job sich erholt) und **stummschalten** (1 h, 1 Tag, bis Erholung). Beides steht sichtbar an Kachel und Job und überlebt den Neustart. |
| M11 | S | Testmeldung je Kanal und je Regel per Knopf; die Job-Seite zeigt die wirksamen Regeln (aus Job oder Profil). |

### 4.4 Oberfläche

| Nr. | Prio | Anforderung |
|---|---|---|
| O1 | M | **Tafel** (Startseite): Kacheln je Job und Wächter mit Zustand, Zusammenfassung, Verlauf, letztem und nächstem Lauf. Störungen oben und doppelt breit. Filter: Alle, Braucht dich, Jobs, Wächter, Tags. |
| O2 | M | **Fahrplan**: gestern, heute, morgen als Zeitachse, eine Spur je Job; Läufe als Marken, Geplantes hohl, Überfälliges markiert; Achsen, Legende, Tooltips. |
| O3 | M | **Job-Seite**: Einstellungen (nur lesen), wirksame Meldungsregeln, Dauer je Lauf als Diagramm mit Achsen und Legende, Liste der Läufe, Knöpfe nach J9, J24, M10, M11. |
| O4 | M | **Lauf-Seite**: Zustand, Exit-Code, Dauer, Auslöser, Ausgabe, gesendete Meldungen. |
| O5 | M | Gestaltung aus Kante, dunkel und hell, auch am Telefon. Texte deutsch und englisch über Kataloge wie in Andon. |
| O6 | M | Uhrzeiten mit Zone, wo der Browser abweicht (`tzMark` wie in Andon). |
| O7 | M | Die Oberfläche bearbeitet keine Jobs. Quelle bleibt das Repo. Nur Pause, Bestätigen, Stummschalten, Ping-Token, API-Token und Geheimnisse entstehen in der Oberfläche; sie liegen in `/data`. |
| O8 | S | Seite **Kanäle**: Zustand der Kanäle, letzter Versand, Profile, Ruhezeit, letzte Meldungen, Testknopf. |
| O9 | M | Ladefehler (ungültige Jobs, fehlerhafte `jidoka.toml`) stehen sichtbar oben, mit Datei und Zeile. |
| O11 | M | Seite **Geheimnisse**: Name, welche Jobs ihn nennen, zuletzt geändert; Setzen, Ersetzen, Löschen. Werte sind nur schreibbar, nie wieder sichtbar. Genannte, aber fehlende Geheimnisse und ungenutzte stehen oben. Jede Änderung steht im Protokoll (wer, wann, welcher Name, nie der Wert). |
| O12 | M | Seite **Zugang**: API-Token anlegen (einmal angezeigt), Rechte, zuletzt genutzt, löschen (Z2). |
| O13 | S | Zeitpläne in Worten („täglich 06:00“, „alle 5 min“), der Cron-Ausdruck im Tooltip. Texte der Ladefehler übersetzt. |
| O10 | M | Demo-Modus mit Beispieljobs für Screenshots und QA, nach der Regel `demo-screenshots`; nie in der Auslieferung. |

### 4.5 Zugang und Sicherheit

| Nr. | Prio | Anforderung |
|---|---|---|
| Z1 | M | Nur über die Webseite. Anmeldung über Authentik (OIDC) wie Andon. Wer zugreifen darf, entscheidet Authentik (Richtlinie der Anwendung); Jidoka prüft keine Konten oder Gruppen. Optional schränken `OIDC_ALLOW_SUB` oder `OIDC_ALLOW_GROUP` zusätzlich ein. |
| Z2 | M | API nur mit Token. Token entstehen auf der Seite „Zugang“, werden einmal angezeigt und gehasht in `/data` gespeichert. Rechte je Token: `read`, `run`, `reload`. Andon bekommt `read` (später `run`), Komodo nur `reload`. |
| Z3 | M | Jeder Job läuft unter einer eigenen UID, nicht der des Servers und nicht der eines anderen Jobs, ohne Docker-Socket. Die Datenbank, `MASTER_KEY` und die Umgebung des Servers sind für sie nicht lesbar; jeder Job bekommt nur seine genannten Geheimnisse (J7) und seinen Zustandsordner (J22). |
| Z4 | M | Jede schreibende Aktion der Oberfläche mit CSRF-Schutz und nur per POST: Starten, Probelauf, Pausieren, Abbrechen, Bestätigen, Stummschalten, Neuladen, Testmeldung, Ping-Token erneuern, API-Token anlegen und löschen, Geheimnisse setzen und löschen. |
| Z5 | M | Geheimnisse von Jidoka selbst (SMTP-Passwort, Apprise-Schlüssel, Andon-Hook-URL, Herzschlag-URL) liegen wie die der Jobs verschlüsselt in der Datenbank. Nur was vor der ersten Anmeldung nötig ist, kommt als Umgebungsvariable von Komodo: `MASTER_KEY` und das OIDC-Client-Secret. Nie in `jidoka.toml`, Log, Seite oder API. |
| Z7 | M | Verschlüsselung wie in Andon (`internal/crypto`, übernommen nach I10): Schlüssel = Argon2id(`MASTER_KEY`, Salt in `/data`), Werte in Spalten `*_enc`. `MASTER_KEY` liefert Komodo als Secret aus; eine Kopie liegt in Vaultwarden. Ohne ihn startet Jidoka, aber Jobs mit Geheimnissen scheitern mit klarer Meldung, und die Seite zeigt oben, dass der Schlüssel fehlt. Meldungen gehen dann nur über Kanäle, die kein Geheimnis brauchen; Jidokas eigener Herzschlag (B7) fehlt, sein Ausbleiben meldet Uptime Kuma. `jidoka rotate-key` verschlüsselt alle Werte mit einem neuen Schlüssel. |
| Z6 | M | Das Jobs-Repo ist Codeausführung: Wer nach `jidoka-jobs` pushen kann, führt Code auf ploetze mit Netz und Job-Geheimnissen aus. Schreibrecht hat nur der Eigentümer; jedes Neuladen mit Änderungen meldet sich (J20). |

### 4.6 Integration mit Andon

| Nr. | Prio | Anforderung |
|---|---|---|
| I1 | M | Jidoka-API, Zeiten in RFC 3339 (UTC), Listen mit `limit`/`before`: `GET /api/v1/status` (Version, Commit der Jobs, letztes Neuladen, Zahl ungültiger Jobs, Zustand der Kanäle, letzter Takt des Planers), `GET /api/v1/jobs` (Jobs **und** Wächter: Zustand, Grund, letzter und nächster Lauf, die letzten 16 Läufe, Zusammenfassung, Ende der letzten Ausgabe, Tags, Zeitplan, URL der Job-Seite; dazu der Stand wie `/status`, damit Andon eine Anfrage je Abruf braucht), `GET /api/v1/jobs/{id}/runs`, `GET /api/v1/runs/{id}`. Ohne Geheimnisse. |
| I2 | M | Andon: Heartbeats als gemeinsame **Form mit Schnittstelle** (`Heartbeat`, `HeartbeatSource`), bereichsweit gelesen und vereinigt wie die Sicherungen, nicht als `caps.Members`-Kategorie (dort zählt nur das erste Mitglied). Healthchecks und Jidoka liefern beide. Die Form bekommt einen Grund (`gescheitert`, `ausgeblieben`), Zusammenfassung und Link. |
| I3 | M | Umbau in Andon als eigener PR **vor** der Jidoka-Quelle: `heartbeat.down`, `cross.heartbeat_backup`, die Kachel `heartbeats`, `metrics/ops` und `services/hosts` lesen die Form statt `HealthchecksDataset`. Danach nehmen externe Wächter (W2) von selbst am Abgleich mit Borg teil. |
| I4 | M | Andon-Quelle `jidoka`: Fähigkeiten in `caps`, Demo-Daten. Hinweise: `heartbeat.down` mit Grund deckt gescheitert und ausgeblieben; eigene Jidoka-Regeln nur für `ungültig` und gestörte Kanäle; „Jidoka nicht erreichbar“ wie bei jeder Verbindung. |
| I5 | M | Andon-Kachel: Jidoka-Jobs erscheinen in der Heartbeats-Kachel; Detail-Popup mit Verlauf (Legende), Ausgabe-Ende, Link zum Lauf in Jidoka. |
| I6 | S | Jidoka meldet Zustandswechsel an Andons Webhook (`POST /hooks/{id}/{sig}`, `Traits{Webhooks: true}`); Andon verwirft dann den Cache und zeigt den Hinweis sofort. Die Hook-URL ist ein Geheimnis (Z5). |
| I7 | S | Gemeinsamer Auftritt: gleiche Anmeldung, Kante, Sprachen, Zonenmarke. Im Kopf von Jidoka ein Link zu Andon, die Andon-Kachel verlinkt den Job. |
| I8 | K | „Jetzt ausführen“ aus Andon: Schreiboperation in Andons `caps` (`caps.Store`), mit Bestätigung und Audit, über einen Token mit Recht `run`. |
| I9 | M | Unabhängigkeit: Jidoka meldet selbst, auch wenn Andon aus ist. Andon läuft ohne Jidoka. |
| I10 | M | Übernommener Code aus Andon (OIDC, i18n, `tzMark`, Hooks-Muster) nennt im Kopf der Datei seine Herkunft (Pfad und Commit in Andon). |

### 4.7 Betrieb

| Nr. | Prio | Anforderung |
|---|---|---|
| B1 | M | Ein Container, Image `ghcr.io/shrippen/jidoka`, gebaut von der CI auf dem GitHub-Spiegel wie bei Andon. Betrieb auf **ploetze** neben Andon; Compose in `docker-compose-ploetze/jidoka/`, ausgeliefert von Komodo. |
| B2 | M | Volumes: `/data` (SQLite mit Geheimnissen, Zustand, Python-Pakete, Token, Salt). Ein Port. Umgebung von Komodo: `MASTER_KEY`, OIDC-Client-Secret, `JOBS_REPO_TOKEN`; dazu `BASE_URL` (Adresse von Jidoka, z. B. `https://jidoka.intern.arianw.de`), `JOBS_REPO`, `JOBS_BRANCH`. |
| B3 | M | Zeit in UTC gespeichert. |
| B4 | S | Leerlauf unter 30 MB RAM. Höchstens 2 Läufe gleichzeitig (einstellbar). Weitere Termine warten in einer Schlange; die Wartezeit zählt nicht zum Timeout. Wer länger wartet als sein Abstand zum nächsten Termin, entfällt (J5). |
| B5 | M | `/healthz` antwortet nur 200, wenn der Planer in der letzten Minute getaktet hat und die Datenbank schreibbar ist. |
| B6 | S | Nach einem Push nach `jidoka-jobs` ruft Komodo (oder ein Gitea-Webhook) `POST /api/v1/reload` mit einem Token (Recht `reload`) auf; Jidoka holt das Repo dann selbst (J1). |
| B7 | M | Ausfall von Jidoka oder ploetze fällt außerhalb auf: Jidoka sendet alle 5 min einen Herzschlag an einen Push-Monitor in Uptime Kuma (eredin); Komodo (eredin) überwacht den Container. |
| B8 | M | Fehlerhafte `jidoka.toml`: Beim Start und beim Neuladen bleibt die letzte gültige Fassung (in `/data`) in Kraft; der Fehler steht oben (O9) und geht über die alten Kanäle raus. Ohne jede gültige Fassung startet Jidoka ohne Kanäle und zeigt das. Eine `jidoka.toml` ohne Profil `standard` ist ungültig: Jobs ohne `[[notify]]`, `reload`, `invalid` und ein gescheitertes Holen (J1) melden über dieses Profil. Eine fehlende `jidoka.toml` ist ein Fehler wie eine ohne Profil `standard`; `jidoka run` braucht sie nicht. |
| B9 | M | `/data` wird mit dem vorhandenen Borgmatic von ploetze gesichert, samt Salt. Ohne `MASTER_KEY` ist die Sicherung für Geheimnisse wertlos, darum liegt er in Komodo und Vaultwarden. Ein Verlust von `/data` kostet Verlauf, Pause, Stummschaltung, Token und Geheimnisse; danach kann je Störung eine Meldung doppelt kommen. |

### 4.8 Werkzeuge

| Nr. | Prio | Anforderung |
|---|---|---|
| T1 | S | `jidoka validate <ordner>` aus demselben Binary: prüft `jidoka.toml`, alle `job.toml` und `watch.toml` und zeigt die nächsten drei Termine je Job. Läuft als CI im Repo `jidoka-jobs`. |
| T2 | S | `jidoka run <id>` lokal im selben Image, mit Geheimnissen aus einer lokalen, nicht eingecheckten Datei: Jobs entwickeln ohne Deploy. |

## 5. Technik (Vorschlag)

Wie Andon, damit Wissen und Werkzeuge geteilt werden: Go,
`net/http`, `html/template`, htmx, SQLite, Kante eingebunden. Eigenes
Repo, kein gemeinsames Go-Modul mit Andon. Was beide brauchen, wird
übernommen, nicht verlinkt (I10).

## 6. Nicht-Ziele

- Keine Workflow-Engine: keine Abhängigkeiten zwischen Jobs, keine DAGs.
- Keine verteilten Runner, kein Ausführen auf anderen Hosts, kein Container je Job.
- Kein Editor für Jobs in der Oberfläche.
- Keine Sammlung von Rohdaten; Jobs schreiben in ihre Zieldienste.
- Keine Rechte und Rollen für mehrere Benutzer.
- Keine Metriken für Prometheus, keine Vorlagen für Meldungstexte,
  keine Kalenderansicht der Zeitpläne über den Fahrplan hinaus.

## 7. Erster Job: Shinkiryu-Kalender

- Liest `https://www.shinkiryu.de/shinkiryu/de/Seminars.php`, schreibt
  jeden Lehrgang per CalDAV in den Kalender
  `http://cal.arianw.de/dav/calendars/shinki/Lehrgaenge` in Davis.
- **Inhalt:** Titel „<Lehrgang> – <Ort>“, Ort, Infozeile; hat die Zeile
  eine Ausschreibung, steht ihr Link im Feld `URL` und in der
  Beschreibung.
- **Datum:** Ganztagstermine, `DTEND` exklusiv (Tag nach dem letzten
  Lehrgangstag). Die Seite nennt das Jahr nicht immer und wechselt die
  Trenner („–“, „bis“); das Jahr folgt aus der Reihenfolge der Zeilen
  (aufsteigend, Jahreswechsel bei kleinerem Monat).
- **UID** aus Startdatum und Ort, mit fester Vorsilbe
  (`shinkiryu-2026-11-21-hamburg@jidoka`). Ein korrigierter Titel ist
  dann eine Änderung, kein Löschen und Neuanlegen.
- **Schreiben nur bei Änderung:** Der Job vergleicht den Inhalt und
  schreibt mit `If-Match` (ETag), sonst nichts; eigene Notizen am Termin
  bleiben, solange die Seite sich nicht ändert.
- **Löschen nur in der Zukunft:** Verschwindet ein Lehrgang von der
  Seite, löscht der Job ihn nur, wenn sein Beginn in der Zukunft liegt
  und die UID seine Vorsilbe trägt. Vergangene Lehrgänge bleiben im
  Kalender.
- **Abbrechen statt Schaden:** Jede Zeile, die sich nicht lesen lässt,
  ist ein Fehler. 0 Lehrgänge, oder mehr als 3 bzw. 30 % Löschungen in
  einem Lauf, brechen ohne Schreiben ab.
- Zusammenfassung „11 Lehrgänge, 2 neu, 1 geändert, 0 entfernt“; neue
  Lehrgänge als Nachricht in `$JIDOKA_NOTIFY` (Auslöser `change`).

## 8. Abnahme

Jeder Fall ist ein automatischer Test (Go) oder ein Schritt in `QA.md`.

| Fall | Erwartung |
|---|---|
| Sommerzeit März, Termin 02:30 | Läuft einmal um 03:00. |
| Winterzeit Oktober, Termin 02:30 | Läuft einmal. |
| Container-Stopp mitten im Lauf | Lauf `abgebrochen`, keine Fehlermeldung, Nachholen nach Start (J10). |
| Skript startet Kind, das sich löst, und hängt | Timeout beendet beide; kein Zombie bleibt. |
| Job liest die Datenbank oder `/proc` des Servers | Zugriff verweigert. |
| Job nennt ein Geheimnis nicht | Er bekommt es nicht. |
| Genanntes Geheimnis fehlt | Lauf scheitert vor dem Start, Meldung nennt den Namen. |
| Wert eines Geheimnisses auf Seite, API, Log | Nirgends sichtbar, auch nicht nach dem Setzen. |
| `MASTER_KEY` fehlt oder falsch | Start klappt, Jobs mit Geheimnissen scheitern mit klarer Meldung. |
| Geheimnis in Ausgabe, roh und Base64 | Erscheint als `***`. |
| Apprise nicht erreichbar | Nach 3 Versuchen Mail statt Apprise, Kanal oben als gestört. |
| Störung 02:15, Erholung 03:15, Ruhezeit 22–07 | Eine Meldung um 07:00 mit beidem. |
| `retries = 2`, `after = 2`, täglich | Meldung am 2. Tag mit Störung, nicht nach dem 2. Versuch. |
| `job.toml` beim Neuladen kaputt | Job `ungültig`, Meldung, andere Jobs unberührt. |
| `jidoka.toml` kaputt | Letzte gültige Fassung gilt, Fehler oben. |
| Neuladen, während ein Push ankommt | Kein Job halb geladen. |
| Gitea beim Start nicht erreichbar | Letzter geholter Stand gilt, Fehler oben, Meldung. Ohne jeden geholten Stand: keine Jobs, Fehler oben. |
| Ping mit unbekanntem Token, 100 Pings je Sekunde | 404, Drosselung greift. |
| Andon-Kachel bei 0, 1 und 50 Jobs | Eine Anfrage an Jidoka je Abruf. |

## 9. Entscheidungen

- Anmeldung über Authentik; wer zugreifen darf, regelt Authentik, Jidoka prüft das nicht (11.11.2026, ersetzt „ein Benutzer“).
- Die Jobs kommen aus einem eigenen Git-Repo `jidoka-jobs` (11.11.2026).
- Externe Wächter mit Ping-URL sind gewollt (11.11.2026).
- Jobs in Python mit `uv` und sh/bash (11.11.2026).
- Mehr Einstellungen je Job, vor allem für Meldungen: Regeln, Profile,
  Ruhezeit, Sammelmeldung, Bestätigen und Stummschalten (11.11.2026).
- Jobs zur Laufzeit neu laden, ohne Neustart des Containers (11.11.2026).
- Apprise und SMTP auf eredin reichen als Meldewege (11.11.2026).
- Sammelmeldungen nur für Erfolg und Erholung, Störungen immer sofort
  (11.11.2026).
- Jidoka läuft auf ploetze neben Andon; ein Ausfall fällt über Uptime
  Kuma und Komodo auf eredin auf (11.11.2026).
- Compose in `docker-compose-ploetze/jidoka/` (11.11.2026).
- Geheimnisse in der Oberfläche, verschlüsselt wie in Andon, Schlüssel
  über Komodo; Jobs nennen ihre Geheimnisse (11.11.2026).
- Review-Vorschläge mit M und S übernommen, K-Vorschläge als K
  (11.11.2026).
- Jidoka holt das private Jobs-Repo selbst mit Token; Repo, Branch und
  Adresse von Jidoka (`https://jidoka.intern.arianw.de`) sind per
  Umgebung einstellbar. Komodo ruft nach einem Push das Neuladen auf
  (11.11.2026).
- Sprache der Meldungen per `locale` in `jidoka.toml`; Ladefehler
  übersetzt; Zeitpläne in Worten (11.11.2026).
- Andon liest Jidoka mit einer Anfrage je Abruf (11.11.2026).
- Erster Job schreibt nach `http://cal.arianw.de/dav/calendars/shinki/Lehrgaenge`,
  mit Link zur Ausschreibung, wo es einen gibt (11.11.2026).

## 10. Offene Fragen

- Davis ist unter `http://cal.arianw.de` angegeben: Benutzer und
  Passwort gingen per Basic-Auth unverschlüsselt über das Netz. Gibt es
  eine `https`-Adresse, oder bleibt der Weg im LAN?
