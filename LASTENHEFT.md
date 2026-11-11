# Lastenheft Jidoka

Stand: 11.11.2026, Entwurf. Mit *Annahme* markierte Punkte sind meine
Vorschläge und warten auf deine Bestätigung (siehe „Offene Fragen“).

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
 TrueNAS
 ┌──────────────────────────────── Container jidoka ─────────────────┐
 │                                                                   │
 │  /jobs (nur lesen)          Planer ──► Läufer ──► Prozess (Skript) │
 │   jidoka.toml   ─ Laden ─►    │           │ Exit, Ausgabe, Dauer   │
 │   shinkiryu/                  │           ▼                        │
 │     job.toml                  │        Läufe (SQLite, /data)       │
 │     main.py                   ▼           │                        │
 │   …                        Wächter ───────┤ Zustandswechsel        │
 │                            (Ausbleiben)   ▼                        │
 │                                        Melder ──► Apprise, SMTP    │
 │  Web (Kante, htmx) ◄── Übersicht, Job, Lauf                        │
 │  API (Token)       ◄── Andon                                       │
 └───────────────────────────────────────────────────────────────────┘
        ▲ Anmeldung: Authentik (OIDC)
```

## 3. Begriffe

| Begriff | Bedeutung |
|---|---|
| Job | Ein Ordner unter `/jobs` mit `job.toml` und einem ausführbaren Skript. |
| Lauf | Eine Ausführung eines Jobs: Start, Ende, Zustand, Ausgabe, Zusammenfassung. |
| Zustand | `ok`, `fehler`, `zeitüberschreitung`, `läuft`, `ausgeblieben`, `pausiert`, `ungültig`. |
| Wächter | Erwartet in einem Zeitfenster einen erfolgreichen Lauf (Totmannschalter). |
| Kanal | Ziel einer Meldung: Apprise oder E-Mail. |
| Auslöser | Wann gemeldet wird: Fehler, Erfolg, Erholung, Ausbleiben. |

## 4. Anforderungen

Priorität: **M** muss, **S** soll, **K** kann.

### 4.1 Jobs

| Nr. | Prio | Anforderung                                                                                                                                                                                         |
| --- | ---- | --------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| J1 | M | Jobs sind Ordner unter `/jobs` (Bind-Mount, nur lesen) aus dem Git-Repo `jidoka-jobs` auf Gitea. Jidoka liest sie beim Start und beim Neuladen (J20). |
| J2  | M    | Ein Job, dessen `job.toml` fehlerhaft ist, verhindert den Start nicht. Er erscheint als `ungültig` mit Fehlertext auf der Seite und löst eine Meldung aus.                                          |
| J3  | M    | Zeitplan als Cron-Ausdruck (5 Felder) in der Zone des Jobs, Standard `Europe/Berlin`. Umstellung auf Sommer- und Winterzeit ohne doppelten oder fehlenden Lauf.                                     |
| J4  | M    | Erfolg heißt Exit-Code 0, alles andere ist `fehler`. Nach dem Timeout (Standard 10 min) wird die ganze Prozessgruppe beendet, Zustand `zeitüberschreitung`.                                         |
| J5  | M    | Ein Job läuft nie zweimal gleichzeitig. Fällt ein Termin in einen laufenden Lauf, entfällt er und wird vermerkt.                                                                                    |
| J6  | M    | Ausgabe (stdout und stderr, zeitlich verschränkt) wird je Lauf gespeichert, gekürzt auf 256 KB (Anfang und Ende bleiben).                                                                           |
| J7  | M    | Geheimnisse kommen als Umgebungsvariablen aus `/secrets/<job>.env` (eigener Mount, nicht im Git). Ihre Werte werden in Ausgabe, Seite, API und Meldung durch `***` ersetzt.                    |
| J8  | S    | Ein Skript kann eine Zeile Zusammenfassung in `$JIDOKA_SUMMARY` schreiben, z. B. „41 Lehrgänge, 2 neu, 1 entfernt“. Sie steht in Übersicht und Meldung.                                             |
| J9  | S    | Jobs von Hand starten und pausieren, über die Seite. Pause überlebt den Neustart.                                                                                                                   |
| J10 | S    | `catchup = true`: Ein Termin, der verpasst wurde, weil der Container aus war, wird nach dem Start einmal nachgeholt.                                                                                |
| J11 | S    | Laufzeit im Image: `sh`, `bash`, Python 3, `curl`, `jq`. Python über `uv`: Abhängigkeiten im Skriptkopf (PEP 723), zwischengespeichert unter `/data`. Dazu `sh` und `bash`. |
| J12 | S    | Aufbewahrung: Läufe 180 Tage, höchstens 500 je Job; danach gelöscht.                                                                                                                                |
| J13 | S    | Wiederholung nach Fehler (`retries`, `retry_delay`), gemeldet wird erst nach dem letzten Versuch.                                                                                                   |
| J14 | S | `jitter`: zufällige Verzögerung bis zur angegebenen Dauer, damit Jobs fremde Server nicht immer zur selben Sekunde treffen. |
| J15 | S | `lock`: Jobs mit gleichem Namen laufen nie gleichzeitig (z. B. zwei Jobs, die in Davis schreiben). |
| J16 | S | `args` und `[env]`: Argumente und Umgebungsvariablen ohne Geheimnis. Jidoka setzt zusätzlich `JIDOKA_JOB`, `JIDOKA_RUN`, `JIDOKA_TRIGGER` (Zeitplan, Hand, Nachholen, Wiederholung) und `JIDOKA_LAST_OK` (Zeit des letzten Erfolgs). |
| J17 | S | `description`, `link`, `tags`: Beschreibung und Ziel-Link auf der Job-Seite, Filter nach Tags auf der Tafel. |
| J18 | S | `keep` je Job: Aufbewahrung abweichend vom Standard. |
| J19 | S | `enabled = false`: Job bleibt sichtbar, läuft aber nicht (anders als Pause: im Repo festgelegt). |
| J20 | M | Neuladen zur Laufzeit, ohne Neustart: Knopf „Jobs neu laden“, `POST /api/v1/reload` (Token) und optional Beobachten des Ordners (`watch = true` in `jidoka.toml`). Jidoka liest alle Jobs, prüft sie und tauscht erst dann aus. Laufende Läufe enden mit der alten Fassung. Das Ergebnis steht auf der Seite: neu, geändert, entfernt, ungültig, dazu der Commit von `/jobs`, falls es ein Git-Ordner ist. |
| J21 | M | Ein entfernter Job verschwindet von der Tafel; seine Läufe bleiben bis zum Ende der Aufbewahrung unter „Entfernt“ lesbar. Pause, Bestätigen und Stummschalten bleiben am Job-Namen hängen, falls er zurückkommt. |

Beispiel `job.toml` (alle Felder außer `run` und `schedule` sind
optional):

```toml
name        = "Shinkiryu-Lehrgänge → Kalender"
description = "Lehrgänge von shinkiryu.de in den Davis-Kalender Aikido"
link        = "https://davis.arianw.de/calendars/aikido"  # Ziel, auf der Job-Seite verlinkt
tags        = ["aikido", "kalender"]
run         = "main.py"
args        = ["--kalender", "aikido"]
schedule    = "0 6 * * *"
zone        = "Europe/Berlin"
jitter      = "5m"        # zufällige Verzögerung, schont fremde Server
timeout     = "5m"
retries     = 2           # bei Fehler noch zweimal versuchen …
retry_delay = "10m"       # … im Abstand von 10 min
catchup     = true
lock        = "davis"     # Jobs mit gleichem lock laufen nie gleichzeitig
expect      = "26h"       # Wächter: ein Erfolg je 26 h
keep        = "365d"      # Aufbewahrung der Läufe, abweichend vom Standard

[env]                     # Werte ohne Geheimnis; Geheimnisse in /secrets
DAVIS_URL = "https://davis.arianw.de/dav/"

[[notify]]                # Regel 1: Störungen sofort aufs Handy und per Mail
on       = ["fail", "timeout", "missed"]
after    = 2              # erst ab dem 2. Fehler in Folge
channels = ["apprise:handy", "mail"]
remind   = "24h"
priority = "high"         # durchbricht Ruhezeiten

[[notify]]                # Regel 2: Erholung nur per Mail
on       = ["recover"]
channels = ["mail"]

[[notify]]                # Regel 3: Erfolge gesammelt, einmal je Woche
on       = ["success"]
channels = ["mail"]
digest   = "weekly"
```

Ohne `[[notify]]` gilt das Profil `standard` aus `jidoka.toml`. Ein Job
kann auch `notify_profile = "leise"` setzen:

```toml
# /jobs/jidoka.toml
quiet = "22:00-07:00"     # Ruhezeit: Meldungen warten bis 07:00 (außer priority = "high")

[channels.apprise]
url  = "http://apprise-eredin:8000/notify/jidoka"
tags = { handy = "handy", desktop = "kde" }   # apprise:handy → Apprise-Tag "handy"

[channels.mail]
smtp = "smtp.eredin:587"
from = "jidoka@arianw.de"
to   = ["arian@…"]

[[profiles.standard]]
on = ["fail", "timeout", "missed"]
channels = ["apprise", "mail"]
remind = "24h"

[[profiles.standard]]
on = ["recover"]
channels = ["apprise"]

[[profiles.leise]]
on = ["fail", "missed"]
after = 3
channels = ["mail"]
digest = "daily"
```

### 4.2 Wächter (Totmannschalter)

| Nr. | Prio | Anforderung                                                                                                                                                                                                            |
| --- | ---- | ---------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| W1  | M    | `expect` je Job: Gab es im Zeitfenster keinen erfolgreichen Lauf, wird der Job `ausgeblieben`. Das deckt auch einen hängenden Planer oder einen Job ab, der still nichts tut.                                          |
| W2  | S    | Externe Wächter für Arbeit außerhalb Jidokas (z. B. Borg, Snapshots auf TrueNAS): eine Datei `watch.toml` unter `/jobs`, Ping über `POST /ping/<token>` (und `/start`, `/fail`). Erreichbar im LAN und über Tailscale. |

### 4.3 Meldungen

| Nr. | Prio | Anforderung |
|---|---|---|
| M1 | M | Kanäle Apprise (Apprise-API auf eredin) und E-Mail (SMTP), eingestellt in `/jobs/jidoka.toml`. Apprise-Tags als Unterkanäle (`apprise:handy`), Mail mit eigenen Empfängern je Regel (`to`). |
| M2 | M | Auslöser: `fail`, `timeout`, `success`, `recover` (erster Erfolg nach Störung), `missed`, `invalid` (Job nicht geladen), `start` (Lauf beginnt), `slow` (Lauf dauert länger als `slow = "…"`). |
| M3 | M | Regeln je Job (`[[notify]]`): welche Auslöser, welche Kanäle, ab dem wievielten Fehler in Folge (`after`), Erinnerung (`remind`, `"never"` möglich), Priorität (`low`, `normal`, `high`; wird zum Apprise-Typ). Ohne Regeln gilt das Profil `standard`. |
| M4 | M | Profile in `jidoka.toml`: benannte Regelsätze, die Jobs mit `notify_profile` übernehmen. Regeln im Job ersetzen das Profil ganz, mischen nicht. |
| M5 | M | Keine Flut: Bei anhaltender Störung nur die erste Meldung, dann Erinnerungen nach `remind`. Ein Neustart wiederholt keine Meldung, die schon gesendet ist. Wiederholungen eines Laufs (`retries`) melden erst nach dem letzten Versuch. |
| M6 | M | Ruhezeit (`quiet`) global, je Job überschreibbar: Meldungen warten bis zum Ende und gehen dann gebündelt raus. `priority = "high"` gilt sofort. |
| M7 | S | Sammelmeldung (`digest = "daily"` oder `"weekly"`, Zeitpunkt einstellbar): eine Meldung mit allen Ereignissen der Regel statt vieler einzelner. |
| M8 | M | Inhalt: Job, Zustand, Zeit mit Zone, Dauer, Zusammenfassung, letzte Zeilen der Ausgabe (`tail`, Standard 20), Link zum Lauf. Mail optional mit ganzer Ausgabe als Anhang (`attach_output = true`). |
| M9 | M | Scheitert der Versand, wird er wiederholt und der Fehler auf der Seite gezeigt. Scheitert ein Kanal dauerhaft, geht die Meldung über den anderen (Ausweichkanal). |
| M10 | S | In der Oberfläche, ohne das Repo zu ändern: Störung **bestätigen** (keine Erinnerungen mehr, bis der Job sich erholt) und **stummschalten** (1 h, 1 Tag, bis Erholung). Beides steht sichtbar an Kachel und Job und überlebt den Neustart. |
| M11 | S | Testmeldung je Kanal und je Regel per Knopf; die Job-Seite zeigt die wirksamen Regeln (aus Job oder Profil). |

### 4.4 Oberfläche

| Nr. | Prio | Anforderung                                                                                                                                           |
| --- | ---- | ----------------------------------------------------------------------------------------------------------------------------------------------------- |
| O1  | M    | Übersicht: alle Jobs mit Zustand, letztem Lauf, nächstem Lauf, Zusammenfassung und Verlauf der letzten Läufe. Was Aufmerksamkeit braucht, steht oben. |
| O2  | M    | Job-Seite: Zeitplan, Wächter, Auslöser, Kanäle (nur lesen), Liste der Läufe.                                                                          |
| O3  | M    | Lauf-Seite: Zustand, Exit-Code, Dauer, Ausgabe.                                                                                                       |
| O4  | M    | Gestaltung aus Kante, dunkel und hell, auch am Telefon. Texte deutsch und englisch über Kataloge wie in Andon.                                        |
| O5  | M    | Uhrzeiten mit Zone, wo der Browser abweicht (`tzMark` wie in Andon).                                                                                  |
| O6 | M | Die Oberfläche bearbeitet keine Jobs. Quelle bleibt der Ordner. Nur Pause, Bestätigen und Stummschalten (M10) entstehen in der Oberfläche; sie liegen in `/data`, nicht im Repo. |
| O7  | S    | Seite „Kanäle“: Zustand der Kanäle, letzter Versand, Testknopf.                                                                                       |
| O8  | S    | Ungültige Jobs und Ladefehler vom Start stehen sichtbar oben.                                                                                         |

### 4.5 Zugang und Sicherheit

| Nr. | Prio | Anforderung                                                                                   |
| --- | ---- | --------------------------------------------------------------------------------------------- |
| Z1  | M    | Nur über die Webseite. Anmeldung über Authentik (OIDC) wie Andon. Ein Benutzer, keine Rollen. |
| Z2  | M    | API nur mit Token (lesen, optional ausführen). Token werden gehasht gespeichert.              |
| Z3  | M    | Jobs laufen als unprivilegierter Benutzer, ohne Docker-Socket.                                |

### 4.6 Integration mit Andon

| Nr. | Prio | Anforderung |
|---|---|---|
| I1 | M | Jidoka-API: `GET /api/v1/jobs` (Zustand, letzter und nächster Lauf, Zusammenfassung, Wächter), `GET /api/v1/jobs/{id}/runs`, `GET /api/v1/runs/{id}` (ohne Geheimnisse). |
| I2 | M | Andon bekommt die Integration `jidoka`: Quelle, Fähigkeiten in `caps`, Demo-Daten. Healthchecks und Jidoka halten dieselbe Art Daten. Daraus wird die Andon-Kategorie *Heartbeats* (zweiter Dienst, nach Andons Regel). Regeln der Kategorie laufen für beide. |
| I3 | M | Andon-Hinweise: Job gescheitert, Job ausgeblieben, Jidoka nicht erreichbar. Andon ist damit der Wächter über Jidoka. |
| I4 | M | Andon-Kachel „Jobs“ mit Detail-Popup: Läufe als Verlauf mit Legende, Auszug der Ausgabe, Link zum Lauf in Jidoka. |
| I5 | S | Jidoka meldet Zustandswechsel per Webhook an Andon, damit der Hinweis sofort erscheint und nicht erst beim nächsten Abruf. |
| I6 | S | Gemeinsamer Auftritt: gleiche Anmeldung (Authentik), Kante, Sprachen, Zonenmarke. Im Kopf von Jidoka steht ein Link zu Andon, die Andon-Kachel verlinkt den Job. |
| I7 | K | „Jetzt ausführen“ aus Andon als Schreiboperation mit Bestätigung und Audit, über den Token mit Ausführungsrecht. |
| I8 | M | Unabhängigkeit: Jidoka meldet selbst, auch wenn Andon aus ist. Andon läuft ohne Jidoka. |

### 4.7 Betrieb

| Nr. | Prio | Anforderung |
|---|---|---|
| B1 | M | Ein Container, Image `ghcr.io/shrippen/jidoka`, gebaut von der CI auf dem GitHub-Spiegel wie bei Andon. Betrieb auf TrueNAS (regis). Die Compose-Datei liegt in `docker-compose-regis/jidoka/`, Komodo liefert sie aus. |
| B2 | M | Volumes: `/jobs` und `/secrets` (nur lesen), `/data` (SQLite, Python-Pakete). Ein Port. Konfiguration: `/jobs/jidoka.toml` und Umgebungsvariablen für Geheimnisse. |
| B3 | M | Zeit in UTC gespeichert. |
| B4 | S | Leerlauf unter 30 MB RAM. Höchstens 2 Läufe gleichzeitig (einstellbar). |
| B5 | S | `/healthz` für Docker und Andon. |
| B6 | S | Komodo checkt `jidoka-jobs` auf regis aus (Repo-Ressource). Nach einem Push zieht Komodo das Repo und ruft `POST /api/v1/reload` auf; ein Neustart ist nicht nötig. |

## 5. Technik (Vorschlag)

Wie Andon, damit Wissen und Werkzeuge geteilt werden: Go,
`net/http`, `html/template`, htmx, SQLite, Kante eingebunden. Eigenes
Repo, kein gemeinsames Go-Modul mit Andon. Was beide brauchen, wird
übernommen, nicht verlinkt.

## 6. Nicht-Ziele

- Keine Workflow-Engine: keine Abhängigkeiten zwischen Jobs, keine DAGs.
- Keine verteilten Runner, kein Ausführen auf anderen Hosts.
- Kein Editor für Jobs in der Oberfläche.
- Keine Sammlung von Rohdaten; Jobs schreiben in ihre Zieldienste.
- Keine Rechte und Rollen für mehrere Benutzer (vorerst).

## 7. Erster Job: Shinkiryu-Kalender

- Liest `https://www.shinkiryu.de/shinkiryu/de/Seminars.php`, schreibt
  jeden Lehrgang per CalDAV `PUT` in einen Kalender in Davis.
- Feste UID je Lehrgang (aus Datum, Ort und Titel), damit wiederholte
  Läufe keine Dubletten erzeugen.
- Lehrgänge, die von der Seite verschwinden, löscht er. Er löscht nur
  Einträge mit seiner UID-Vorsilbe.
- 0 gefundene Lehrgänge sind ein Fehler: dann wird nichts gelöscht.
- Zusammenfassung: „41 Lehrgänge, 2 neu, 1 geändert, 0 entfernt“.

## 8. Entscheidungen

- Anmeldung über Authentik, ein Benutzer (11.11.2026).
- `/jobs` kommt aus einem eigenen Git-Repo `jidoka-jobs` (11.11.2026).
- Externe Wächter mit Ping-URL sind gewollt (11.11.2026).
- Compose in `docker-compose-regis/jidoka/`, ausgeliefert von Komodo
  (11.11.2026).
- Jobs in Python mit `uv` und sh/bash (11.11.2026).
- Mehr Einstellungen je Job, vor allem für Meldungen: Regeln, Profile,
  Ruhezeit, Sammelmeldung, Bestätigen und Stummschalten (11.11.2026).
- Jobs zur Laufzeit neu laden, ohne Neustart des Containers (11.11.2026).

## 9. Offene Fragen

Keine.
