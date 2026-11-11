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
 │   jidoka.toml   ─ Start ─►    │           │ Exit, Ausgabe, Dauer   │
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

| Nr. | Prio | Anforderung |
|---|---|---|
| J1 | M | Jobs sind Ordner unter `/jobs` (Bind-Mount, nur lesen). Jidoka liest sie beim Start. Hinzufügen, Entfernen und Ändern wirken nach einem Neustart des Containers. |
| J2 | M | Ein Job, dessen `job.toml` fehlerhaft ist, verhindert den Start nicht. Er erscheint als `ungültig` mit Fehlertext auf der Seite und löst eine Meldung aus. |
| J3 | M | Zeitplan als Cron-Ausdruck (5 Felder) in der Zone des Jobs, Standard `Europe/Berlin`. Umstellung auf Sommer- und Winterzeit ohne doppelten oder fehlenden Lauf. |
| J4 | M | Erfolg heißt Exit-Code 0, alles andere ist `fehler`. Nach dem Timeout (Standard 10 min) wird die ganze Prozessgruppe beendet, Zustand `zeitüberschreitung`. |
| J5 | M | Ein Job läuft nie zweimal gleichzeitig. Fällt ein Termin in einen laufenden Lauf, entfällt er und wird vermerkt. |
| J6 | M | Ausgabe (stdout und stderr, zeitlich verschränkt) wird je Lauf gespeichert, gekürzt auf 256 KB (Anfang und Ende bleiben). |
| J7 | M | Geheimnisse kommen als Umgebungsvariablen aus einer Datei neben dem Job (`secrets.env`, nicht im Git). Ihre Werte werden in Ausgabe, Seite, API und Meldung durch `***` ersetzt. |
| J8 | S | Ein Skript kann eine Zeile Zusammenfassung in `$JIDOKA_SUMMARY` schreiben, z. B. „41 Lehrgänge, 2 neu, 1 entfernt“. Sie steht in Übersicht und Meldung. |
| J9 | S | Jobs von Hand starten und pausieren, über die Seite. Pause überlebt den Neustart. |
| J10 | S | `catchup = true`: Ein Termin, der verpasst wurde, weil der Container aus war, wird nach dem Start einmal nachgeholt. |
| J11 | S | Laufzeit im Image: `sh`, `bash`, Python 3, `curl`, `jq`. Python-Abhängigkeiten je Job über `requirements.txt`, beim Start in eine venv unter `/data` installiert und zwischengespeichert. *Annahme* |
| J12 | S | Aufbewahrung: Läufe 90 Tage, höchstens 500 je Job; danach gelöscht. |
| J13 | K | Wiederholung nach Fehler (`retries`, `retry_delay`), gemeldet wird erst nach dem letzten Versuch. |

Beispiel `job.toml`:

```toml
name     = "Shinkiryu-Lehrgänge → Kalender"
run      = "main.py"
schedule = "0 6 * * *"
timeout  = "5m"
expect   = "26h"                         # Wächter: ein Erfolg je 26 h
notify   = ["fail", "recover", "missed"] # Auslöser
channels = ["apprise", "mail"]
```

### 4.2 Wächter (Totmannschalter)

| Nr. | Prio | Anforderung |
|---|---|---|
| W1 | M | `expect` je Job: Gab es im Zeitfenster keinen erfolgreichen Lauf, wird der Job `ausgeblieben`. Das deckt auch einen hängenden Planer oder einen Job ab, der still nichts tut. |
| W2 | S | Externe Wächter für Arbeit außerhalb Jidokas (z. B. Borg, Snapshots auf TrueNAS): eine Datei `watch.toml` unter `/jobs`, Ping über `POST /ping/<token>` (und `/start`, `/fail`). *Annahme: nur im LAN erreichbar.* |

### 4.3 Meldungen

| Nr. | Prio | Anforderung |
|---|---|---|
| M1 | M | Kanäle Apprise (Apprise-API auf eredin, URL und Schlüssel) und E-Mail (SMTP). Global in `/jobs/jidoka.toml`, je Job wählbar. |
| M2 | M | Auslöser je Job: `fail`, `success`, `recover` (erster Erfolg nach Fehler), `missed`. Standard: `fail`, `recover`, `missed`. |
| M3 | M | Keine Flut: Bei anhaltendem Fehler nur die erste Meldung, danach eine Erinnerung alle 24 h (einstellbar). Ein Neustart wiederholt keine Meldung, die schon gesendet ist. |
| M4 | M | Inhalt: Job, Zustand, Zeit mit Zone, Dauer, Zusammenfassung, letzte 20 Zeilen Ausgabe, Link zum Lauf. |
| M5 | M | Scheitert der Versand, wird er wiederholt und der Fehler auf der Seite gezeigt. |
| M6 | S | Testmeldung je Kanal per Knopf. |

### 4.4 Oberfläche

| Nr. | Prio | Anforderung |
|---|---|---|
| O1 | M | Übersicht: alle Jobs mit Zustand, letztem Lauf, nächstem Lauf, Zusammenfassung und Verlauf der letzten Läufe. Was Aufmerksamkeit braucht, steht oben. |
| O2 | M | Job-Seite: Zeitplan, Wächter, Auslöser, Kanäle (nur lesen), Liste der Läufe. |
| O3 | M | Lauf-Seite: Zustand, Exit-Code, Dauer, Ausgabe. |
| O4 | M | Gestaltung aus Kante, dunkel und hell, auch am Telefon. Texte deutsch und englisch über Kataloge wie in Andon. |
| O5 | M | Uhrzeiten mit Zone, wo der Browser abweicht (`tzMark` wie in Andon). |
| O6 | M | Die Oberfläche bearbeitet keine Jobs. Quelle bleibt der Ordner. |
| O7 | S | Seite „Kanäle“: Zustand der Kanäle, letzter Versand, Testknopf. |
| O8 | S | Ungültige Jobs und Ladefehler vom Start stehen sichtbar oben. |

### 4.5 Zugang und Sicherheit

| Nr. | Prio | Anforderung |
|---|---|---|
| Z1 | M | Nur über die Webseite. Anmeldung über Authentik (OIDC) wie Andon. *Annahme: ein Benutzer, keine Rollen.* |
| Z2 | M | API nur mit Token (lesen, optional ausführen). Token werden gehasht gespeichert. |
| Z3 | M | Jobs laufen als unprivilegierter Benutzer, ohne Docker-Socket. |

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
| B1 | M | Ein Container, Image `ghcr.io/shrippen/jidoka`, gebaut von der CI auf dem GitHub-Spiegel wie bei Andon. Betrieb auf TrueNAS. |
| B2 | M | Volumes: `/jobs` (nur lesen), `/data` (SQLite, venvs). Ein Port. Konfiguration: `/jobs/jidoka.toml` und Umgebungsvariablen für Geheimnisse. |
| B3 | M | Zeit in UTC gespeichert. |
| B4 | S | Leerlauf unter 30 MB RAM. Höchstens 2 Läufe gleichzeitig (einstellbar). |
| B5 | S | `/healthz` für Docker und Andon. |

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

## 8. Offene Fragen

1. Anmeldung über Authentik, ein Benutzer: einverstanden?
2. Woher kommt `/jobs`: ein Git-Repo `jidoka-jobs` auf Gitea, auf
   TrueNAS ausgecheckt (Vorschlag), oder ein Ordner im Dataset?
3. Externe Wächter (W2): gebraucht, oder reicht Healthchecks dafür?
4. Python als Hauptsprache für Jobs, Bash für Kleinkram: passt das?
5. TrueNAS-Compose: in eines der Repos `docker-compose-*` oder ein neues?
