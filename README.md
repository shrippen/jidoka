# Jidoka

Führt kleine Skripte nach Zeitplan aus, zeigt ihren Stand auf einer Seite
und meldet über Apprise und E-Mail, wenn ein Lauf scheitert, gelingt oder
ausbleibt. Geschwister von [Andon](https://github.com/shrippen/andon):
Andon zeigt den Zustand, Jidoka hält an und meldet (自働化, Toyota-
Produktionssystem). Jidoka meldet selbst und braucht Andon nicht; Andon
liest Jidoka über eine API.

Die Jobs liegen nicht in Jidoka, sondern als Ordner in einem Git-Repo
(Beispiel: `jidoka-jobs`). Jidoka holt es selbst. Die Oberfläche
bearbeitet keine Jobs.

Anforderungen und Entscheidungen: [`LASTENHEFT.md`](LASTENHEFT.md).

Inhalt:

1. [Betrieb mit Docker](#1-betrieb-mit-docker)
2. [Umgebungsvariablen](#2-umgebungsvariablen)
3. [Anmeldung mit OIDC](#3-anmeldung-mit-oidc)
4. [Das Jobs-Repo](#4-das-jobs-repo)
5. [`jidoka.toml`](#5-jidokatoml)
6. [Einen Job anlegen](#6-einen-job-anlegen)
7. [Externe Wächter](#7-externe-wächter)
8. [Lokal ausprobieren](#8-lokal-ausprobieren)
9. [Meldungen und Oberfläche](#9-meldungen-und-oberfläche)
10. [API](#10-api)
11. [Schlüssel wechseln](#11-schlüssel-wechseln)
12. [Entwicklung](#12-entwicklung)

## 1. Betrieb mit Docker

### Image

`ghcr.io/shrippen/jidoka`, für `linux/amd64` und `linux/arm64`, signiert
mit cosign. Die CI auf dem GitHub-Spiegel baut es.

| Tag | Inhalt |
|---|---|
| `edge` | Stand von `main` |
| `X.Y.Z`, `X.Y`, `latest` | Release-Tags; erst ab dem ersten Release (es gibt noch keinen) |

Das Image enthält `sh`, `bash`, `curl`, `jq`, `git`, Python 3 und `uv`
(Alpine 3.24) sowie die Zonendaten. Der Server ist ein statisches
Binary.

### Compose

Beispiel (`deploy/compose.example.yaml`):

```yaml
services:
  jidoka:
    image: ghcr.io/shrippen/jidoka:edge
    container_name: jidoka
    hostname: jidoka
    restart: unless-stopped
    stop_grace_period: 45s
    volumes:
      - /docker/appdata/jidoka/data:/data
    environment:
      TZ: Europe/Berlin
      MASTER_KEY: ${MASTER_KEY}
      OIDC_ISSUER: ${OIDC_ISSUER}
      OIDC_CLIENT_ID: ${OIDC_CLIENT_ID}
      OIDC_CLIENT_SECRET: ${OIDC_CLIENT_SECRET}
      BASE_URL: https://jidoka.intern.arianw.de
      JOBS_REPO: https://git.arianw.de/shrippen/jidoka-jobs.git
      JOBS_BRANCH: main
      JOBS_REPO_TOKEN: ${JOBS_REPO_TOKEN}
      TRUSTED_PROXY: 172.16.0.0/12
    networks:
      - jidoka
networks:
  jidoka:
    driver: bridge
    name: jidoka
```

Werte in `${…}` kommen aus der Umgebung des Deployments (z. B. einer
`.env`-Datei), nie aus Git. Das Beispiel veröffentlicht keinen Port: Ein
Reverse Proxy im Netz `jidoka` erreicht Port 8080. Ohne Proxy `ports:
["8080:8080"]` ergänzen; die Anmeldung braucht trotzdem HTTPS (siehe
[Hinter einem Reverse Proxy](#hinter-einem-reverse-proxy)).

### Volume `/data`

Ein Volume, `/data`. Es enthält:

| Pfad | Inhalt |
|---|---|
| `jidoka.db`, `jidoka.db-wal`, `jidoka.db-shm` | SQLite im WAL-Modus: Läufe, Meldungen, Zustand, Geheimnisse (Spalten `*_enc`, verschlüsselt), API-Token (als Hash), Ping-Token (verschlüsselt) |
| `jidoka.db.salt` | Salt des Schlüssels, kein Geheimnis, aber nötig |
| `jobs/` | Checkout des Jobs-Repos (nur mit `JOBS_REPO`) |
| `state/<id>/` | Zustandsordner eines Jobs (`$JIDOKA_STATE`), bleibt über Läufe |
| `home/<id>/` | `$HOME` eines Jobs, darin der `uv`-Cache dieses Jobs |
| `tmp/run-<lauf>/` | `$TMPDIR` eines Laufs, danach gelöscht |

Der Container startet als root, setzt nur die Besitzer unter `/data`
(Docker legt ein gebundenes Verzeichnis als root an) und führt Jidoka
danach als UID 10001 aus (`entrypoint.sh`). `/data` selbst ist `0711`:
Jobs laufen hindurch, listen aber nichts auf.

### Eigene UID je Job, kein `no-new-privileges`

Jeder Job läuft unter einer eigenen UID und GID, gezählt ab
`JIDOKA_UID_BASE` (im Image 20000). Ein Job kann deshalb weder die
Datenbank noch `MASTER_KEY` oder die Umgebung des Servers lesen, auch
nicht den Zustandsordner eines anderen Jobs. Die UID eines Jobs wird bei
seinem ersten Lauf vergeben und in der Datenbank gespeichert.

Das Binary trägt dafür Datei-Capabilities (`cap_setuid`, `cap_setgid`,
`cap_kill`, `cap_chown`). `security_opt: no-new-privileges` blendet sie
aus; **nicht setzen**. Auch kein `user:` setzen: Der Einstieg braucht
root, um `/data` zu prüfen.

### Stoppen

Bei SIGTERM beendet Jidoka laufende Jobs (SIGTERM an die Prozessgruppe,
nach 10 s SIGKILL) und vermerkt sie als `aborted`, ohne Meldung. Es
wartet höchstens 30 s. Docker bricht nach 10 s ab; darum
`stop_grace_period: 45s`. Läufe, die beim Start noch `running` heißen,
werden `aborted`.

### Healthcheck

Das Image prüft alle 30 s (`--timeout=5s`, `--start-period=20s`) mit
`jidoka healthcheck`. Der Befehl fragt `http://127.0.0.1:<Port aus LISTEN>/healthz`.
`/healthz` antwortet nur mit 200, wenn der Planer in der letzten Minute
getaktet hat und die Datenbank Schreibzugriff gibt. Die Route braucht
keine Anmeldung.

### Hinter einem Reverse Proxy

- **HTTPS ist Pflicht.** Die Cookies sind `Secure`, der Server sendet
  HSTS. Über reines HTTP ist keine Anmeldung möglich.
- `BASE_URL` auf die öffentliche Adresse setzen (z. B.
  `https://jidoka.intern.arianw.de`). Daraus folgen die Redirect-URI der
  Anmeldung, die Links in Meldungen und die URLs der Ping-Adressen. Ohne
  `BASE_URL` (und ohne `base_url` in `jidoka.toml`) nimmt Jidoka Schema
  und Host der Anfrage (`X-Forwarded-Proto`, `Host`).
- `TRUSTED_PROXY`: kommagetrennte Adressen oder CIDRs der Proxys, deren
  `X-Forwarded-For` gilt. Die Drosselung der Pings zählt je Client-Adresse;
  ohne diese Angabe sieht sie nur die Adresse des Proxys. Gelesen wird von
  rechts: die erste Adresse, die nicht zu den Proxys gehört. Ein
  fehlerhafter Eintrag wird im Log gemeldet und übersprungen.

### Sicherung

- `jidoka.db` samt `-wal`/`-shm` und `jidoka.db.salt` **zusammen** sichern.
  Ohne Salt lassen sich die Geheimnisse nicht mehr öffnen.
- `MASTER_KEY` **getrennt** aufbewahren (Passwortmanager). Die Sicherung
  ohne ihn ist für Geheimnisse und Ping-Token wertlos; wer beides
  zusammen hat, kann sie öffnen.
- `jobs/` muss nicht gesichert werden: Jidoka holt es neu aus dem Repo.
- Ein Verlust von `/data` kostet Verlauf, Pausen, Stummschaltungen,
  API-Token, Ping-Token und Geheimnisse (Lastenheft B9).

## 2. Umgebungsvariablen

Nur was vor der Anmeldung nötig ist, steht in der Umgebung. Alles
andere steht in `jidoka.toml` oder ist ein in der Oberfläche gesetztes
Geheimnis.

| Name | Standard | Zweck |
|---|---|---|
| `MASTER_KEY` | – | Hauptschlüssel der Verschlüsselung (Argon2id mit dem Salt neben der Datenbank). Ohne ihn startet Jidoka mit einer Warnung; Jobs mit Geheimnissen scheitern, Kanäle mit Geheimnis (Apprise-Schlüssel, SMTP-Passwort), Herzschlag und Andon-Hook gehen nicht, neue Wächter bekommen keine Ping-Adresse. Beim ersten Start mit Schlüssel entsteht der Salt. |
| `NEW_MASTER_KEY` | – | Nur für `jidoka rotate-key` (siehe [Schlüssel wechseln](#11-schlüssel-wechseln)). |
| `DATA_DIR` | `/data` | Ordner für Datenbank, Salt, Checkout, Zustand. |
| `LISTEN` | `:8080` | Adresse des Webservers. Auch `jidoka healthcheck` liest sie. |
| `BASE_URL` | – | Öffentliche Adresse; überschreibt `base_url` aus `jidoka.toml`. |
| `JOBS_REPO` | – | HTTPS-URL des Jobs-Repos. Gesetzt: Jidoka holt es nach `$DATA_DIR/jobs`. |
| `JOBS_BRANCH` | `main` | Branch des Jobs-Repos. |
| `JOBS_REPO_TOKEN` | – | Token für das Repo, optional (öffentliche Repos). |
| `JOBS_DIR` | `/jobs` | Jobs-Ordner, **nur ohne** `JOBS_REPO` (Entwicklung). |
| `OIDC_ISSUER` | – | Issuer oder dessen Konfigurations-URL. |
| `OIDC_CLIENT_ID` | – | Client-ID des OIDC-Providers. |
| `OIDC_CLIENT_SECRET` | – | Client-Secret. |
| `OIDC_ALLOW_SUB` | – | Optional: nur dieses Konto (Claim `sub`). |
| `OIDC_ALLOW_GROUP` | – | Optional: nur Mitglieder dieser Gruppe (Claim `groups`). |
| `TRUSTED_PROXY` | – | Proxys, deren `X-Forwarded-For` gilt (Adressen oder CIDRs, mit Komma). |
| `JIDOKA_UID_BASE` | Image: `20000`; ohne Angabe: `0` | Erste UID für Jobs. `0` heißt: kein Benutzerwechsel, Jobs laufen als der Server (nur Entwicklung). |
| `JIDOKA_SHIM` | Image: `/usr/local/bin/jidoka-shim` | Pfad des Shims, der jeden Job umschließt (ohne Angabe: das eigene Binary). Nicht ändern. |
| `TZ` | – | Zeitzone des Containers. Die Zonen der Jobs stehen in `jidoka.toml` und `job.toml`; Jidoka selbst liest `TZ` nicht. |

Ohne `OIDC_ISSUER`, `OIDC_CLIENT_ID` und `OIDC_CLIENT_SECRET` gibt es
keine Anmeldung: Niemand kommt auf die Seiten.

## 3. Anmeldung mit OIDC

Jidoka meldet über OpenID Connect an (Authorization-Code-Fluss mit
PKCE S256, Scopes `openid profile email`). Wer zugreifen darf,
entscheidet der Identity-Provider; Jidoka prüft keine Konten. Es gibt
keine Rollen: Wer angemeldet ist, darf alles. Eine Sitzung gilt 30 Tage
ab der letzten Nutzung.

### Provider anlegen (Authentik als Beispiel)

1. In Authentik einen OAuth2/OpenID-Provider und eine Anwendung
   anlegen (z. B. Slug `jidoka`). Client-Typ vertraulich.
2. **Redirect-URI** (genau): `<BASE_URL>/auth/oidc/callback`, z. B.
   `https://jidoka.intern.arianw.de/auth/oidc/callback`.
3. **Issuer-Modus** des Providers: „Each provider has a different
   issuer“. Der Issuer ist dann `https://auth.arianw.de/application/o/jidoka/`.
   Jidoka verlangt, dass die Metadaten (`/.well-known/openid-configuration`)
   genau diesen Issuer nennen; sonst bleibt die Anmeldung geschlossen
   (die Seite meldet „nicht erreichbar“, das Log nennt den Grund).
4. Client-ID und Client-Secret in `OIDC_CLIENT_ID` und
   `OIDC_CLIENT_SECRET` eintragen.
5. In Authentik die Richtlinie der Anwendung so setzen, dass nur
   berechtigte Konten zugreifen dürfen.

### `OIDC_ISSUER`

Zwei Schreibweisen gelten:

```
OIDC_ISSUER=https://auth.arianw.de/application/o/jidoka/
OIDC_ISSUER=https://auth.arianw.de/application/o/jidoka/.well-known/openid-configuration
```

Die Konfiguration wird eine Stunde zwischengespeichert. Der ID-Token
wird auf Signatur (RS256, ES256; HS256 gegen das Client-Secret), `iss`,
`aud`, `nonce` und Ablauf geprüft.

### Einschränken

Standard: Jeder, den der Provider anmeldet, kommt hinein. Zusätzlich:

- `OIDC_ALLOW_SUB=<sub>`: nur dieses Konto.
- `OIDC_ALLOW_GROUP=<gruppe>`: nur Mitglieder dieser Gruppe. Jidoka liest
  dazu den Claim `groups` des ID-Tokens (Liste von Strings).

Sind beide gesetzt, genügt eines. Wer ausgeschlossen wird, bekommt 403.

## 4. Das Jobs-Repo

Quelle der Jobs ist ein Git-Repo. Struktur:

```
jidoka-jobs/
├── jidoka.toml            Einstellungen, Kanäle, Profile (Pflicht)
├── shinkiryu/             ein Job: Ordnername = Id
│   ├── job.toml
│   └── run.sh
└── borg/
    └── watch.toml         ein externer Wächter
```

Ein Ordner ohne `job.toml` und ohne `watch.toml` wird ignoriert, ebenso
Ordner mit Punkt am Anfang. Die Id ist der Ordnername, erlaubt sind
`[a-z0-9-]+`. Wer den Ordner umbenennt, legt einen neuen Job an (Pause,
Bestätigung und Verlauf hängen an der Id).

### Holen

Mit `JOBS_REPO` holt Jidoka das Repo selbst nach `$DATA_DIR/jobs`:
beim Start und bei jedem Neuladen, flach (`--depth 1`), dann
`reset --hard` und `clean`. Änderungen im Checkout gehen verloren.

- Nur `https`.
- `JOBS_REPO_TOKEN` geht nur in die Umgebung von `git` (als Kopfzeile
  `Authorization: token <Token>`). Er steht in keiner URL, keiner
  `.git/config` und keiner Datei, die ein Job lesen kann. Für Gitea ein
  Token mit Leserecht auf das Repo.
- `JOBS_BRANCH` ist der Branch (Standard `main`).
- Ein Holen bricht nach einer Minute ab.

Ohne `JOBS_REPO` liest Jidoka den Ordner `JOBS_DIR` (Standard `/jobs`)
und holt nichts. Das ist für die Entwicklung gedacht. Die Dateien müssen
für die UIDs der Jobs lesbar sein, die Skripte ausführbar.

### Neuladen

- Knopf „Jobs neu laden“ auf der Tafel.
- `POST /api/v1/reload` mit einem Token mit Recht `reload` (z. B. aus
  dem Deployment-Werkzeug nach einem Push, siehe [API](#10-api)).

Jidoka holt das Repo, liest alle Jobs, prüft sie und tauscht erst dann
aus. Laufende Läufe enden mit der alten Fassung. Das Ergebnis (neu,
geändert, entfernt, ungültig, Commit) steht auf der Seite. Ein Neuladen,
das Jobs ändert, löst `reload` aus. Geändert heißt: eine Datei im
Ordner des Jobs hat sich geändert, auch das Skript.

### Fehler

- **Holen scheitert** (Gitea nicht erreichbar, Token abgelaufen): Der
  zuletzt geholte Stand gilt weiter. Der Fehler steht oben auf der
  Seite und geht als Meldung raus (Profil `standard`). Danach versucht
  Jidoka es alle 5 Minuten erneut, solange es scheitert.
- **Erster Start ohne geholten Stand:** keine Jobs, keine Kanäle, der
  Fehler steht oben. Eine Meldung ist dann nicht möglich; das Ausbleiben
  des Herzschlags fällt im Push-Monitor auf (siehe
  [Eigener Herzschlag](#eigener-herzschlag)).
- **Ein Job ist fehlerhaft:** Er wird `invalid` (mit Datei und Zeile auf
  der Seite, Meldung `invalid`). Die anderen Jobs laufen weiter. Eine
  ältere gültige Fassung des Jobs läuft nicht weiter.
- **`jidoka.toml` ist fehlerhaft oder fehlt:** siehe
  [Fehlende oder kaputte Datei](#fehlende-oder-kaputte-datei).

Das Repo ist Codeausführung: Wer nach `jidoka-jobs` pushen darf, führt
Code im Container aus, mit Netz und den Geheimnissen der Jobs. Schreibrecht
nur für den Eigentümer.

## 5. `jidoka.toml`

Liegt im Wurzelordner des Jobs-Repos. Alle Schlüssel sind optional, außer
dass ein Profil `standard` vorhanden sein muss. Unbekannte Schlüssel sind
ein Fehler (ebenso in `job.toml` und `watch.toml`). Keine Geheimnisse in
diese Datei: Passwörter und Schlüssel stehen auf der Seite „Geheimnisse“.

```toml
zone      = "Europe/Berlin"     # Zone der Ruhezeit und der Sammelmeldungen; Standard Europe/Berlin
locale    = "de"                # Sprache der Meldungen: de oder en; Standard de
quiet     = "22:00-07:00"       # Ruhezeit (HH:MM-HH:MM, in `zone`)
heartbeat = "5m"                # eigener Herzschlag; ohne Angabe aus
max_parallel = 2                # gleichzeitige Läufe; Standard 2, mindestens 1
keep      = "180d"              # Aufbewahrung der Läufe; Standard 180d
keep_runs = 500                 # höchstens so viele Läufe je Job; Standard 500
base_url  = "https://jidoka.intern.arianw.de"   # Links in Meldungen; BASE_URL gewinnt
andon_url = "https://andon.example.org"         # Link „Andon“ im Kopf der Seiten

[channels.apprise]
url  = "http://apprise:8000/notify"             # Pflicht; der Schlüssel ist das Geheimnis APPRISE_KEY
tags = { handy = "handy", desktop = "kde" }     # apprise:handy -> Apprise-Tag "handy"

[channels.mail]
smtp = "smtp.example.org:587"   # Pflicht, host:port
user = "jidoka"                 # optional; mit user ist das Geheimnis SMTP_PASSWORD nötig
from = "jidoka@example.org"     # Pflicht
to   = ["admin@example.org"]    # Standard-Empfänger

[[profiles.standard]]           # Pflicht: das Profil "standard"
on       = ["fail", "timeout", "missed", "invalid", "reload"]
channels = ["apprise", "mail"]
remind   = "24h"

[[profiles.standard]]
on       = ["recover"]
channels = ["apprise"]

[[profiles.leise]]              # weiteres Profil, per notify_profile gewählt
on       = ["fail", "missed"]
after    = 3
channels = ["mail"]
```

| Schlüssel | Bedeutung |
|---|---|
| `zone` | IANA-Zone (`Europe/Berlin`). Gilt für `quiet`, `digest` und als Standard der Jobs. Die Zonendaten sind im Binary. |
| `locale` | `de` oder `en`: Sprache der Meldungen. Die Oberfläche folgt dem Browser (`Accept-Language`). |
| `quiet` | Ruhezeit, `HH:MM-HH:MM`; `22:00-07:00` geht über Mitternacht. Anfang und Ende dürfen nicht gleich sein. |
| `heartbeat` | Abstand des eigenen Herzschlags, siehe unten. |
| `max_parallel` | Wie viele Läufe gleichzeitig laufen. Weitere warten in einer Schlange. |
| `keep`, `keep_runs` | Aufbewahrung. Ältere Läufe und Läufe über der Anzahl werden stündlich gelöscht; Läufe entfernter Jobs nach `keep`. |
| `base_url` | Basis der Links, `http` oder `https`. Ohne sie (und ohne `BASE_URL`) haben die Links in Meldungen und API keine Basis. |
| `andon_url` | Link im Kopf der Seiten. |
| `channels.apprise.url` | Basis der Apprise-API. Jidoka sendet `POST <url>/<APPRISE_KEY>` (JSON mit `title`, `body`, `type`, `tag`, `format`). Der Typ folgt dem Auslöser: Störung `failure`, `missed` `warning`, `recover`/`success` `success`, sonst `info`. |
| `channels.apprise.tags` | Unterkanäle: `apprise:<name>` in einer Regel sendet mit dem Tag, den die Tabelle nennt. `apprise` ohne Zusatz sendet ohne Tag. |
| `channels.mail` | SMTP mit STARTTLS (wenn der Server es anbietet). Anmeldung (PLAIN) nur mit `user`; unverschlüsselt zu einem entfernten Host verweigert Jidoka sie. |

### Dauern

Dauern sind Go-Dauern (`90s`, `5m`, `26h`) oder ganze Tage (`365d`).
Negative Werte sind ungültig.

### Profile und Regeln

Ein Profil ist eine Liste von Regeln (`[[profiles.<name>]]`), siehe
[Regeln für Meldungen](#regeln-für-meldungen). Jobs ohne eigene
`[[notify]]`-Regeln nutzen das Profil `standard` oder das in
`notify_profile` genannte. Das Profil `standard` gilt außerdem für
Ereignisse ohne lesbare Job-Regeln: `reload`, `invalid` eines Jobs, dessen
`job.toml` nicht lädt, eine kaputte `jidoka.toml` und ein gescheitertes
Holen des Repos.

### Fehlende oder kaputte Datei

- Die letzte gültige Fassung bleibt in Kraft. Jidoka speichert sie in der
  Datenbank; das gilt auch nach einem Neustart. Der Fehler steht oben
  (Datei und Zeile) und geht über die alten Kanäle raus.
- Gab es nie eine gültige Fassung, läuft Jidoka ohne Kanäle und ohne
  Profile; die Seite zeigt es.
- Eine Datei ohne Profil `standard` ist ungültig; eine fehlende Datei ist
  ein Fehler wie diese.

### Eigener Herzschlag

Mit `heartbeat = "5m"` ruft Jidoka alle 5 Minuten per GET eine
Push-URL auf (z. B. ein Push-Monitor in Uptime Kuma). Bleibt der Ruf aus,
fällt der Ausfall von Jidoka außerhalb auf. Die URL ist das Geheimnis
`HEARTBEAT_URL`; ohne sie, oder ohne `MASTER_KEY`, sendet Jidoka nichts.

### Andon

Das Geheimnis `ANDON_HOOK_URL` ist die Webhook-URL von Andon. Wechselt
der Zustand eines Jobs, sendet Jidoka `POST` mit
`{"source":"jidoka","job":"<id>","state":"<zustand>"}`; Andon zeigt den
Hinweis dann sofort. Ohne das Geheimnis passiert nichts, und Jidoka wartet
nie auf Andon.

## 6. Einen Job anlegen

### Ordner und `job.toml`

Ein Ordner, Name `[a-z0-9-]+`, mit `job.toml` und dem Skript:

```
shinkiryu/
├── job.toml
└── run.sh
```

Pflicht sind `run` und `schedule`. Beispiel mit allen Schlüsseln:

```toml
name        = "Shinkiryu-Lehrgänge → Kalender"   # Anzeige; Standard: die Id
description = "Lehrgänge in den Kalender schreiben"
link        = "https://calendar.example.org/aikido"  # Ziel, auf der Job-Seite verlinkt (http/https)
tags        = ["aikido", "kalender"]               # Filter auf der Tafel

run         = "run.sh"          # Pflicht: Datei im Ordner des Jobs, relativ, ohne ..
args        = ["--kalender", "aikido"]
schedule    = "0 6 * * *"       # Pflicht: Cron mit 5 Feldern
zone        = "Europe/Berlin"   # Zone des Zeitplans; Standard: zone aus jidoka.toml
jitter      = "5m"              # zufällige Verzögerung bis zu dieser Dauer
timeout     = "5m"              # Standard 10m
slow        = "2m"              # Auslöser slow, wenn ein erfolgreicher Lauf länger dauert
retries     = 2                 # so oft noch einmal versuchen; Standard 0
retry_delay = "10m"             # Abstand der Versuche; Standard 5m
catchup     = true              # verpassten Termin nach dem Start nachholen
lock        = "davis"           # Jobs mit gleichem lock laufen nie gleichzeitig
expect      = "26h"             # Wächter: mindestens ein Erfolg in dieser Zeit
keep        = "365d"            # Aufbewahrung dieses Jobs; Standard: keep aus jidoka.toml
quiet       = "22:00-07:00"     # Ruhezeit dieses Jobs (in `zone` des Jobs)
enabled     = true              # false: sichtbar als „deaktiviert“, läuft nicht
notify_profile = "leise"        # Profil statt standard

secrets     = ["DAVIS_USER", "DAVIS_PASSWORD"]   # Namen; Werte nur in Jidoka

[env]                           # feste Umgebungsvariablen, keine Geheimnisse
DAVIS_URL = "https://calendar.example.org/dav/"

[[notify]]                      # eigene Regeln ersetzen das Profil ganz
on       = ["fail", "timeout", "missed"]
after    = 2
channels = ["apprise:handy", "mail"]
remind   = "24h"
urgent   = true

[[notify]]
on       = ["change"]
channels = ["apprise:handy"]

[[notify]]
on       = ["recover", "success"]
channels = ["mail"]
to       = ["admin@example.org"]
digest   = "weekly Mon 08:00"
tail     = 0
```

Alle anderen Schlüssel sind optional.

### Das Skript

`run` wird direkt gestartet, nicht über eine Shell. Es braucht darum eine
Shebang-Zeile und das Ausführbit (`chmod +x`, im Git als `100755`). Das
Arbeitsverzeichnis ist der Ordner des Jobs. Schreiben darf der Job in
`$JIDOKA_STATE`, `$TMPDIR` und `$HOME`.

- **Erfolg** heißt Exit-Code 0. Alles andere ist `fail`.
- **Ausgabe** (stdout und stderr, zeitlich geordnet) wird je Lauf
  gespeichert, bei mehr als 256 KiB bleiben Anfang und Ende.
- Werte aller Geheimnisse ab 6 Zeichen (auch in Base64- und
  URL-Kodierung) ersetzt Jidoka in Ausgabe, Seite, API und Meldung durch
  `***`. Das ist Schadensbegrenzung, keine Sicherheitsgrenze.
- Beim Timeout bekommt die Prozessgruppe SIGTERM, nach 10 s SIGKILL; Zustand
  `timeout`. Ein Shim je Lauf beendet auch Kindprozesse, die sich
  gelöst haben.

**Shell:**

```sh
#!/bin/sh
set -eu
echo "Lauf $JIDOKA_RUN, Auslöser $JIDOKA_TRIGGER"
if [ "$JIDOKA_TRIGGER" = "dry" ]; then
	echo "Probelauf: nichts geschrieben"
	exit 0
fi
curl -fsS "$DAVIS_URL" -u "$DAVIS_USER:$DAVIS_PASSWORD" >/dev/null
echo "11 Lehrgänge, 2 neu" > "$JIDOKA_SUMMARY"
```

**Python mit `uv` (PEP 723):** das Skript selbst ist `run`, mit
Abhängigkeiten im Kopf:

```python
#!/usr/bin/env -S uv run --script
# /// script
# requires-python = ">=3.12"
# dependencies = ["requests"]
# ///
import os
import requests

print("Lauf", os.environ["JIDOKA_RUN"])
```

Das Image enthält Python 3 aus Alpine 3.24; `requires-python` muss zur
installierten Version passen. Der `uv`-Cache liegt je Job unter
`$HOME/.cache/uv`. Für Skripte ohne Abhängigkeiten genügt
`#!/usr/bin/env python3`.

### Umgebung des Jobs

Die Umgebung ist bereinigt; vom Server wird nichts geerbt. Spätere
Zeilen der Tabelle gewinnen:

| Variable | Wert |
|---|---|
| `PATH` | `/usr/local/bin:/usr/bin:/bin` |
| `HOME` | `/data/home/<id>` |
| `TZ` | Zone des Jobs |
| `LANG` | `C.UTF-8` |
| `UV_PYTHON_DOWNLOADS` | `never`: `uv` nimmt das Python des Images |
| Einträge aus `[env]` | wie angegeben |
| genannte Geheimnisse | je Name eine Variable |
| `JIDOKA_JOB` | Id des Jobs |
| `JIDOKA_RUN` | Nummer des Laufs |
| `JIDOKA_TRIGGER` | `schedule`, `manual`, `catchup`, `retry` oder `dry` |
| `JIDOKA_LAST_OK` | Ende des letzten erfolgreichen Laufs (RFC 3339), sonst leer |
| `JIDOKA_STATE` | `/data/state/<id>`: bleibt über Läufe, z. B. für ETag oder Hash |
| `JIDOKA_SUMMARY` | **Pfad** einer Datei: die erste Zeile (bis 200 Zeichen) wird die Zusammenfassung in Tafel und Meldung |
| `JIDOKA_NOTIFY` | **Pfad** einer Datei: Text bis 4 KiB. Ist er nach einem erfolgreichen Lauf nicht leer, gilt der Auslöser `change` |
| `TMPDIR` | `/data/tmp/run-<lauf>`, leer, danach gelöscht; nicht überschreibbar |

`JIDOKA_SUMMARY` und `JIDOKA_NOTIFY` sind Dateipfade: Das Skript
schreibt hinein (`echo … > "$JIDOKA_SUMMARY"`).

`JIDOKA_TRIGGER=dry` kommt vom Knopf „Probelauf“. Das Skript soll dann nur
zeigen, was es täte. Probeläufe lösen keine Meldung aus und zählen nicht
für Zustand und Wächter.

### Geheimnisse

1. Namen in `job.toml` unter `secrets` nennen: `[A-Z][A-Z0-9_]*`.
2. Werte auf der Seite „Geheimnisse“ setzen (Name und Wert). Sie sind nur
   schreibbar, nie wieder sichtbar, und liegen verschlüsselt in der
   Datenbank. Jede Änderung steht im Protokoll (wer, wann, welcher Name,
   nie der Wert).

Der Job bekommt nur die Geheimnisse, die er nennt. Fehlt eines, scheitert
der Lauf, ohne dass das Skript startet; die Meldung nennt den Namen. Ohne
`MASTER_KEY` scheitert jeder Lauf mit Geheimnissen mit klarer Meldung.

Jidoka selbst nutzt feste Namen: `APPRISE_KEY`, `SMTP_PASSWORD`,
`HEARTBEAT_URL`, `ANDON_HOOK_URL`.

### Zeitplan

Fünf Felder: Minute (0-59), Stunde (0-23), Tag des Monats (1-31), Monat
(1-12 oder `JAN`-`DEC`), Wochentag (0-7 oder `SUN`-`SAT`; 0 und 7 sind
Sonntag). In jedem Feld: `*`, Zahl, Liste `1,15`, Bereich `1-5`, Schritt
`*/5`, `1-10/2`, `5/15` (ab 5, alle 15). Sind Tag und Wochentag beide
eingeschränkt, genügt eines (wie Vixie-Cron). Makros wie `@daily` gibt
es nicht.

| Ausdruck | Läuft |
|---|---|
| `0 6 * * *` | täglich 06:00 |
| `*/5 * * * *` | alle 5 Minuten |
| `30 2 * * 1-5` | werktags 02:30 |
| `0 8 1 * *` | am 1. des Monats, 08:00 |
| `0 */4 * * MON,THU` | Montag und Donnerstag, alle 4 Stunden |

Ausgewertet wird in der Zone des Jobs. Sommerzeit: Ein Termin in der
fehlenden Stunde (02:30 im März) läuft um 03:00; in der doppelten Stunde
im Oktober läuft er nur beim ersten Mal. `jidoka validate` zeigt die
nächsten Termine.

Ablauf eines Termins:

- Ein Job läuft nie zweimal gleichzeitig. Fällt ein Termin in einen
  laufenden Lauf, wird er als `skipped` (entfallen) vermerkt.
- `max_parallel` begrenzt die Läufe insgesamt; weitere warten. Die
  Wartezeit zählt nicht zum Timeout. Wer länger wartet, als der nächste
  Termin entfernt ist, entfällt.
- `jitter` verzögert den Start zufällig bis zur angegebenen Dauer; der
  Termin behält die geplante Zeit.
- `lock`: Jobs mit gleichem Namen laufen nie gleichzeitig.
- `catchup = true`: Nach dem Start wird der jüngste verpasste Termin
  einmal nachgeholt, ebenso ein Termin, den ein Stopp mitten im Lauf
  abgebrochen hat. Ohne `catchup` werden verpasste Termine übersprungen.
  Die Zeit einer Pause wird nie nachgeholt.

### Wiederholungen

`retries = 2` versucht nach einem Fehler oder Timeout noch zweimal, im
Abstand `retry_delay` (Standard 5 Minuten). Ein Termin mit allen
Wiederholungen zählt als **ein** Ergebnis; gemeldet wird nach dem letzten
Versuch. Ein von Hand gestarteter Lauf wird nicht wiederholt.

### Wächter mit `expect`

`expect = "26h"`: Gibt es in dieser Zeit keinen erfolgreichen Lauf, wird
der Job `missed` (ausgeblieben) und meldet. Das Fenster zählt ab dem
letzten Erfolg, bei neuen Jobs ab dem ersten Laden, und ruht bei Pause und
`enabled = false`. Nach einem Start gilt eine Karenz von `timeout` +
`jitter` + 5 Minuten, damit `catchup` zuerst nachholen kann. Ein Exit 0
ohne Wirkung erkennt der Wächter nicht; dafür ist `$JIDOKA_SUMMARY`.

### Regeln für Meldungen

Eine Regel ist ein Eintrag `[[notify]]` im Job oder `[[profiles.<name>]]`
in `jidoka.toml`. Regeln im Job **ersetzen** das Profil ganz, sie mischen
sich nicht.

| Schlüssel | Bedeutung |
|---|---|
| `on` | Pflicht: Liste der Auslöser |
| `channels` | Pflicht: `apprise`, `apprise:<tag>` oder `mail` |
| `to` | Empfänger der Mail (statt `channels.mail.to`) |
| `after` | Erst ab dem n-ten **Termin** in Folge mit Störung melden; zählt nur für `fail` und `timeout`. 0 und 1: sofort |
| `remind` | Erinnerung in diesem Abstand, solange die Störung anhält; `"never"` oder fehlend: keine |
| `urgent` | `true`: durchbricht die Ruhezeit |
| `tail` | Anzahl der letzten Ausgabezeilen in der Meldung; Standard 20, `0` keine |
| `digest` | `"daily 08:00"` oder `"weekly Mon 08:00"` (Zone aus `jidoka.toml`) |
| `attach_output` | `true`: ganze Ausgabe als Anhang der Mail |

Auslöser (`on`):

| Auslöser | Wann |
|---|---|
| `fail` | Lauf endet mit Exit-Code ungleich 0; erst nach dem letzten Versuch |
| `timeout` | Lauf überschreitet `timeout`; erst nach dem letzten Versuch |
| `missed` | Job oder Wächter hat im Fenster keinen Erfolg (`expect`, `watch.toml`) |
| `recover` | erster Erfolg nach einer gemeldeten Störung |
| `success` | jeder erfolgreiche Lauf |
| `invalid` | Datei des Jobs oder `jidoka.toml` lädt nicht |
| `reload` | ein Neuladen hat Jobs geändert |
| `change` | erfolgreicher Lauf, bei dem `$JIDOKA_NOTIFY` Text enthält |
| `start` | ein Lauf beginnt (erster Versuch) |
| `slow` | erfolgreicher Lauf dauerte länger als `slow` |

Eine Regel bekommt je Lauf höchstens eine Meldung, in der Reihenfolge
`recover`, `change`, `success`, `slow`.

Regeln, die man kennen muss:

- **Keine Flut:** Bei anhaltender Störung geht nur die erste Meldung
  raus, danach Erinnerungen nach `remind`. Ein Neustart wiederholt keine
  Meldung, die schon gesendet ist.
- **Ruhezeit:** Meldungen warten bis zum Ende und gehen dann gebündelt
  raus, mit dem Endstand. `urgent` gilt sofort.
- **Sammelmeldung (`digest`):** gilt nur für `success` und `recover`.
  Störungen gehen immer einzeln und sofort (außerhalb der Ruhezeit). Ein
  `recover` mit dem Hinweis „Datei geht wieder, Job scheitert noch“
  wartet ebenfalls nicht. `change` und `reload` gehen sofort, auch wenn die
  Regel `digest` nennt.
- Zur Bestätigung und Stummschaltung siehe
  [Meldungen und Oberfläche](#9-meldungen-und-oberfläche).

## 7. Externe Wächter

Ein Wächter prüft Dienste, die Jidoka nicht startet (Sicherungen,
Snapshots): Sie melden sich per Ping, und Jidoka erwartet den Ping in einem
Zeitfenster. Datei `<id>/watch.toml` (ohne `job.toml` im Ordner):

```toml
name           = "Borg-Sicherung"
description    = "Nächtliche Sicherung"
link           = "https://backup.example.org"
tags           = ["backup"]
period         = "24h"          # entweder period ...
# schedule     = "0 3 * * *"    # ... oder schedule (nicht beides)
# zone         = "Europe/Berlin"
grace          = "2h"           # Pflicht: Toleranz über das Fenster hinaus
enabled        = true
notify_profile = "leise"
# [[notify]] wie bei Jobs
```

- Mit `period` gilt: kein erfolgreicher Ping seit `period` + `grace`.
- Mit `schedule`: Auf den jüngsten Termin muss binnen `grace` ein Ping
  folgen.
- Pause und `enabled = false` lassen das Fenster ruhen.

### Ping-Adresse

Jidoka erzeugt je Wächter einen Token beim ersten Ansehen und zeigt die
Adresse auf der Seite des Wächters (mit `BASE_URL` davor). Mit dem Knopf
„Adresse erneuern“ wird sie ersetzt; die alte gilt sofort nicht mehr.

| Ruf | Bedeutung |
|---|---|
| `/ping/<token>` | Erfolg |
| `/ping/<token>/start` | Der Lauf beginnt |
| `/ping/<token>/fail` | Fehlschlag |

`GET` und `POST` gehen. Ein `POST`-Körper bis 10 KB wird zur Ausgabe des
Laufs (Geheimnisse maskiert). Ein zweites `/start` beendet den ersten
Lauf als `aborted`. Ein `/fail` oder Erfolg ohne vorheriges `/start`
legt einen fertigen Lauf an.

```sh
curl -fsS -X POST --data-binary @bericht.txt https://jidoka.intern.arianw.de/ping/<token>
curl -fsS https://jidoka.intern.arianw.de/ping/<token>/fail
```

Unbekannte Token bekommen 404. Je Client-Adresse gilt eine Drosselung
(10 Pings je Sekunde, Stoß bis 20); darüber antwortet Jidoka mit 429 und
`Retry-After: 1`. Die Adresse ersetzt eine Anmeldung: Der Token ist der
Schlüssel. Nicht ins Log oder in Git schreiben.

## 8. Lokal ausprobieren

Beide Befehle laufen aus demselben Binary (oder dem Image) und brauchen
keinen Server.

### `jidoka validate <ordner>`

Prüft `jidoka.toml`, alle `job.toml` und `watch.toml` und listet jeden
Job mit den nächsten drei Terminen. Fehler stehen als `Datei:Zeile:
Meldung` auf stderr; Exit-Code 1, wenn etwas kaputt ist. Damit lässt sich
das Jobs-Repo in der CI prüfen:

```sh
jidoka validate .
```

```sh
docker run --rm -v "$PWD:/jobs:ro" ghcr.io/shrippen/jidoka:edge validate /jobs
```

Die Ausgabe endet mit `N valid, M invalid`. Anders als `run` braucht
`validate` eine `jidoka.toml`.

### `jidoka run [-secrets datei] [-dry] <jobs-ordner> <id>`

Führt einen Job einmal aus, ohne Deploy. Die Optionen stehen **vor** den
Argumenten.

```sh
jidoka run -secrets .secrets.local jidoka-jobs shinkiryu
jidoka run -secrets .secrets.local -dry jidoka-jobs shinkiryu
```

- `-secrets datei`: `NAME=WERT` je Zeile, Leerzeilen und `#`-Zeilen werden
  übersprungen. Nicht einchecken (in `.gitignore` aufnehmen).
- `-dry`: setzt `JIDOKA_TRIGGER=dry`; ohne die Option ist es `manual`. Auch ein Probelauf braucht die Geheimnisse, die der Job nennt.
- Die Ausgabe des Skripts geht auf stdout; Zustand, Dauer, Zusammenfassung
  und Fehler auf stderr. Exit-Code 0 nur bei `ok`.
- `jidoka.toml` ist nicht nötig.
- Eine Wegwerf-Datenbank und ein Wegwerf-Zustandsordner: nichts bleibt
  erhalten, auch `$JIDOKA_STATE` nicht. Keine Meldungen, kein
  Benutzerwechsel.

Zum selben Ergebnis wie im Betrieb: im Image laufen lassen, z. B.
`docker run --rm -v "$PWD:/jobs:ro" ghcr.io/shrippen/jidoka:edge run /jobs shinkiryu`.

## 9. Meldungen und Oberfläche

### Seiten

| Seite | Inhalt |
|---|---|
| Tafel (`/`) | Kacheln je Job und Wächter: Zustand, Zusammenfassung, Verlauf, letzter und nächster Lauf. Störungen oben. Filter: Alle, Braucht dich, Jobs, Wächter, Tags. Knopf „Jobs neu laden“. Ladefehler (ungültige Jobs, kaputte `jidoka.toml`) mit Datei und Zeile oben. |
| Fahrplan (`/plan`) | gestern, heute, morgen: eine Spur je Job, Läufe als Marken, Geplantes hohl |
| Job (`/jobs/<id>`) | Einstellungen, wirksame Regeln, Dauer je Lauf, Liste der Läufe, Knöpfe, bei Wächtern die Ping-Adresse |
| Lauf (`/runs/<id>`) | Zustand, Exit-Code, Dauer, Auslöser, Ausgabe, gesendete Meldungen |
| Kanäle (`/channels`) | Zustand je Kanal, letzte Sendungen, Testknopf |
| Geheimnisse (`/secrets`) | Setzen, Ersetzen, Löschen; Name, zuletzt geändert |
| Zugang (`/access`) | API-Token anlegen und löschen |

Uhrzeiten tragen die Zone, wo der Browser abweicht. Die Oberfläche gibt
es auf Deutsch und Englisch.

### Job-Zustände

`ok`, `fail`, `timeout` (Ergebnis des letzten Laufs), `running`, `never`
(noch kein Lauf), `missed` (ausgeblieben), `paused`, `disabled`
(`enabled = false`), `invalid` (nicht geladen). Ein Lauf endet als `ok`,
`fail`, `timeout`, `aborted` (Stopp oder Absturz, ohne Meldung) oder
`skipped` (entfallen).

### Knöpfe eines Jobs

- **Jetzt ausführen** und **Probelauf** (nur Jobs). Ein laufender oder
  wartender Job nimmt keinen zweiten Lauf an.
- **Pausieren** und Fortsetzen. Die Pause überlebt den Neustart; die Zeit
  der Pause wird nicht nachgeholt.
- Einen laufenden Lauf **abbrechen** (Seite des Laufs).
- **Störung bestätigen:** keine Erinnerungen mehr, bis sich der Job
  erholt.
- **Stumm schalten:** 1 Stunde, 1 Tag oder bis zur Erholung. Stumm
  heißt: keine Meldungen dieses Jobs. „Bis Erholung“ lässt nur die
  Erholungsmeldung durch und endet mit ihr.
- **Testmeldung** je Regel (Job-Seite) und je Kanal (Seite Kanäle).

Pause, Bestätigung und Stummschaltung hängen an der Id und überleben
Neustart und Neuladen; sie ändern das Repo nicht.

### Kanäle und Zustellung

Ein Versand wird je Kanal bis zu 3-mal versucht (sofort, nach 1 und nach
5 Minuten). Danach gilt der Kanal als gestört (Fehler oben auf der Seite,
`/api/v1/status`), und die Meldung geht über den anderen Kanal, sofern er
eingerichtet ist und die Regel ihn nicht ohnehin nennt. Scheitern beide,
bleibt die Meldung in der Datenbank und wird alle 15 Minuten neu
versucht; sie geht spätestens mit dem nächsten erfolgreichen Versand
raus. Ein gestörter Kanal wird alle 5 Minuten einmal probiert und gilt
nach dem ersten Erfolg als gesund. Der Gesundheitszustand der Kanäle
liegt im Speicher; nach einem Neustart gelten beide als gesund, bis ein
Versand scheitert.

Inhalt einer Meldung: Job, Zustand, Zeit mit Zone, Dauer,
Zusammenfassung, letzte Zeilen der Ausgabe, Link zum Lauf.

### Ruhezeit

`quiet` in `jidoka.toml` gilt für alle Jobs, `quiet` im Job überschreibt es.
Meldungen in der Ruhezeit warten bis zum Ende und gehen dann gebündelt
raus: Störung um 02:15 und Erholung um 03:15 ergeben um 07:00 eine
Meldung mit beidem. Regeln mit `urgent = true` gehen sofort.

## 10. API

JSON unter `/api/v1/`, nur mit Token. Token entstehen auf der Seite
„Zugang“: Name, Rechte, einmal angezeigt (Präfix `jdk_`); Jidoka speichert
nur einen Hash. Mitgeschickt als Kopfzeile:

```
Authorization: Bearer jdk_…
```

| Recht | Erlaubt |
|---|---|
| `read` | Lesen: Status, Jobs, Läufe |
| `run` | Jobs starten |
| `reload` | Jobs neu laden |

Typisch: Andon bekommt `read`, das Deployment-Werkzeug nur `reload`.
Ein unbekannter Token ergibt 401 (mit `WWW-Authenticate: Bearer`), ein
Token ohne das Recht 403. Fehler sind `{"error":"<schlüssel>"}`. Zeiten
sind RFC 3339 in UTC, fehlende Werte `null`. Geheimnisse stehen nirgends.

| Route | Recht | Antwort |
|---|---|---|
| `GET /api/v1/status` | `read` | Version, Commit der Jobs, letztes Neuladen, Zahl ungültiger Jobs, Zustand der Kanäle, letzter Takt des Planers |
| `GET /api/v1/jobs?limit=` | `read` | alle Jobs **und** Wächter (`jobs`, `total`, `status`): Zustand, Grund, Zusammenfassung, letzter und nächster Lauf, die letzten 16 Läufe, Ende der Ausgabe, Tags, Zeitplan, URL der Job-Seite |
| `GET /api/v1/jobs/{id}/runs?limit=&before=` | `read` | Läufe eines Jobs, neueste zuerst; `next_before` zum Weiterblättern |
| `GET /api/v1/runs/{id}` | `read` | ein Lauf mit Ausgabe |
| `POST /api/v1/reload` | `reload` | Ergebnis: `added`, `changed`, `removed`, `invalid`, `commit`, `at` |
| `POST /api/v1/jobs/{id}/run` | `run` | 202 `{"job":"<id>"}`; 404 unbekannt, 409 läuft schon oder deaktiviert |

Listen: `limit` Standard 50, höchstens 500; `before` ist eine Lauf-Id.

```sh
curl -fsS -H "Authorization: Bearer $JIDOKA_TOKEN" https://jidoka.intern.arianw.de/api/v1/jobs
curl -fsS -X POST -H "Authorization: Bearer $JIDOKA_TOKEN" https://jidoka.intern.arianw.de/api/v1/reload
```

Ohne Token erreichbar sind nur `/healthz`, die Anmeldung, die statischen
Dateien und die Ping-Adressen.

## 11. Schlüssel wechseln

`jidoka rotate-key` verschlüsselt alle Geheimnisse und Ping-Token mit
einem neuen Schlüssel. Der Salt bleibt. **Den Server vorher stoppen.**

```sh
docker compose stop jidoka
docker compose run --rm -e NEW_MASTER_KEY='<neuer Schlüssel>' jidoka rotate-key
```

`MASTER_KEY` ist dabei der **alte** Schlüssel (aus der Umgebung des
Containers); fehlt er, bricht der Befehl ab. Fehlt `NEW_MASTER_KEY`,
ebenso (Exit-Code 2). Die Umstellung läuft in einer Transaktion. Danach
`MASTER_KEY` in der Umgebung auf den neuen Wert setzen, an der Stelle, wo
er verwaltet wird, und den Container starten. API-Token und Sitzungen
bleiben gültig (sie hängen nicht am Schlüssel).

## 12. Entwicklung

```sh
make check     # gofmt, go vet (mit und ohne -tags release), staticcheck, go test ./...
go test ./...  # nur die Tests
make build     # Release-Binary nach bin/jidoka
scripts/image-test.sh [image]   # braucht Docker
```

`scripts/image-test.sh` baut das Image (ohne Argument als `jidoka:test`),
startet es mit `testdata/image` als Jobs-Ordner und prüft, was ein Job
darf und nicht darf: Er läuft als eigene UID und kann weder Datenbank,
Datenordner, Umgebung und Binary des Servers noch Zustand, Umgebung oder
Home eines anderen Jobs lesen; Zustandsordner und `$TMPDIR` sind
schreibbar; Python und `uv` laufen. Die CI führt ihn aus.

Aufbau und Konventionen: [`agent.md`](agent.md). Anforderungen und
Entscheidungen: [`LASTENHEFT.md`](LASTENHEFT.md). Review-Hinweise:
[`REVIEW.md`](REVIEW.md).
