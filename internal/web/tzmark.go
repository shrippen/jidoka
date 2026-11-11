// Adapted from andon internal/web/render.go, tzMark (468474a).

package web

import (
	"html/template"
	"strconv"
	"strings"
	"time"
)

const secondsPerMinute = 60

// zoneMark is a zone as the page shows it: its name and offset in minutes.
type zoneMark struct {
	Name   string
	Offset int
}

// zoneOf is loc now. Zones without an abbreviation come as "+0530" and
// read as "UTC+0530".
func zoneOf(loc *time.Location) zoneMark {
	name, offset := time.Now().In(loc).Zone()
	if strings.HasPrefix(name, "+") || strings.HasPrefix(name, "-") {
		name = "UTC" + name
	}
	return zoneMark{Name: name, Offset: offset / secondsPerMinute}
}

// tzMark follows a time of day the server formatted: its zone, hidden;
// jidoka.js shows it where the browser's zone differs (O6).
func tzMark(loc *time.Location) template.HTML {
	z := zoneOf(loc)
	return template.HTML(`<span class="tz-mark" data-offset="` + strconv.Itoa(z.Offset) + `" hidden> ` +
		template.HTMLEscapeString(z.Name) + `</span>`)
}
