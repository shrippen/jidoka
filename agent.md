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
- Die globalen Einstellungen (Zone, Kanäle, Profile …) stehen in der Datenbank und werden auf der Seite Einstellungen gesetzt (`services/settings`); das Jobs-Repo enthält nur Jobs.
- Jidoka meldet selbst; es braucht Andon nicht, Andon braucht Jidoka nicht.
- Geheimnisse nie in Log, Ausgabe, Seite oder API.
- Uhrzeiten mit Zone, wo der Browser abweicht (wie Andon, `tzMark`).

## Aufbau
Der Aufbau (Bausteine, Schichten, wo Neues hingehört) steht in `ARCHITECTURE.md`.

## Konventionen
- Zeiten in der Datenbank als UTC-Text (`db.TimeStr`), gelesen mit `db.ParseTime`.
- Die Datenbank ist nicht als Datei verschlüsselt, nur Spalten `*_enc`:
  Jidoka startet auch ohne `MASTER_KEY` (Z7).
- Übernommener Code aus Andon nennt im Dateikopf Pfad und Commit in Andon (I10).
- `make check` vor jedem Commit (`GOTOOLCHAIN=go1.26.10`, sonst bricht staticcheck).
