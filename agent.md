# Jidoka – Projekt-Wissen

Regeln für alle Repos: `RULES.md`. Hier nur das Besondere.

## Zweck
Kleine Skripte nach Zeitplan ausführen, Läufe anzeigen, bei Fehler,
Erfolg oder Ausbleiben melden (Apprise, E-Mail). Geschwister von Andon
(`../andon`): gleiche Technik, gleiche Gestaltung (Kante), enge
Integration. Anforderungen: `LASTENHEFT.md`.

## Grundsätze
- Jobs gehören nicht zu Jidoka: Ordner unter `/jobs`, gelesen beim Start.
  Die Oberfläche bearbeitet keine Jobs.
- Jidoka meldet selbst; es braucht Andon nicht, Andon braucht Jidoka nicht.
- Geheimnisse nie in Log, Ausgabe, Seite oder API.
- Uhrzeiten mit Zone, wo der Browser abweicht (wie Andon, `tzMark`).
