package web

import (
	"fmt"
	"math"
	"strconv"
	"strings"
	"time"

	"jidoka/internal/i18n"
	"jidoka/internal/model"
	"jidoka/internal/services/status"
)

// px is a coordinate in a chart's own units, written with one decimal.
type px float64

func (p px) String() string {
	return strconv.FormatFloat(float64(p), 'f', 1, 64)
}

// Timetable geometry in viewBox units. The labels sit left of laneLeft.
const (
	planWidth     = 1000
	planLeft      = 190
	planRight     = 24
	planTop       = 40
	planRow       = 46
	planBottom    = 36
	planHourStep  = 3
	planRun       = 5.5
	planSlot      = 4.0
	planDense     = 60 // runs or slots in a lane from which marks become a line
	planBar       = 12
	planDenseW    = 1.6
	planDenseH    = 14
	denseOKShare  = .55 // a dense lane draws ok at this share of the height
	planMinRun    = 4
	planOverdueH  = 20
	planNowTop    = 12
	planAxisPad   = 6
	planNameDrop  = 2
	planStateDrop = 12
)

// Shapes of a mark.
const (
	shapeCircle = "circle"
	shapeRect   = "rect"
	shapeLine   = "line"
)

// planMark is one thing on a lane: a run, a planned slot, an overdue window.
type planMark struct {
	Shape      string
	X, Y, W, H px
	X2         px // line end
	R          px
	State      string // Kante data-state; "" for plan and late
	Class      string
	Href       string // a run links to its page
	Tip        string
}

// planLane is one job's row.
type planLane struct {
	Y         px
	Name      string
	NameY     px
	StateY    px
	Href      string
	StateWord string
	Paused    bool
	PausedX   px
	Marks     []planMark
	Notes     []planNote
}

// planNote is a word drawn on a lane, like "overdue".
type planNote struct {
	X, Y  px
	Text  string
	Class string
	Mid   bool
}

// planHour is one gridline.
type planHour struct {
	X     px
	Label string
}

// planView is the timetable of one day (O2), ready to draw.
type planView struct {
	W, H      int
	Hours     []planHour
	Lanes     []planLane
	GridTop   px
	GridBot   px
	LabelY    px
	AxisLabel string
	HasNow    bool
	NowX      px
	NowTop    px
	NowLabel  string
	NowLabelY px
	Left      px
	Right     px
}

// plan lays tt out for the day it names.
func (u ui) plan(tt status.Timetable) planView {
	end := tt.Day.AddDate(0, 0, 1)
	span := end.Sub(tt.Day)
	height := planTop + planRow*len(tt.Lanes) + planBottom

	x := func(t time.Time) px {
		f := float64(t.Sub(tt.Day)) / float64(span)
		f = math.Max(0, math.Min(1, f))
		return px(planLeft + f*float64(planWidth-planLeft-planRight))
	}

	v := planView{
		W: planWidth, H: height, Lanes: []planLane{},
		GridTop: planTop - planAxisPad, GridBot: px(height - planBottom), LabelY: px(height - planStateDrop),
		Left: planLeft, Right: px(planWidth - planRight),
		AxisLabel: u.T("plan.axis", "zone", zoneOf(tt.Zone).Name, "day", tt.Day.Format("02.01.2006")),
	}
	for h := 0; h <= hoursPerDay; h += planHourStep {
		t := tt.Day.Add(time.Duration(h) * time.Hour)
		v.Hours = append(v.Hours, planHour{X: x(t), Label: fmt.Sprintf("%02d:00", h)})
	}

	for i, l := range tt.Lanes {
		v.Lanes = append(v.Lanes, u.lane(tt, l, i, end, x))
	}

	if !tt.Now.Before(tt.Day) && tt.Now.Before(end) {
		v.HasNow, v.NowX = true, x(tt.Now)
		v.NowTop = px(planTop - planNowTop)
		v.NowLabelY = px(planTop - planNowTop - 2)
		v.NowLabel = u.T("plan.now", "time", tt.Now.In(tt.Zone).Format("15:04"))
	}
	return v
}

func (u ui) lane(tt status.Timetable, l status.Lane, i int, end time.Time, x func(time.Time) px) planLane {
	y := px(planTop + i*planRow + planRow/2)
	out := planLane{
		Y: y, Name: l.Name, NameY: y - planNameDrop, StateY: y + planStateDrop,
		Href: "/jobs/" + l.ID, Marks: []planMark{}, Notes: []planNote{},
		StateWord: u.word("state.", string(l.State)),
	}
	if l.Removed {
		out.StateWord = u.T("plan.removed")
	}

	var inDay []status.Mark
	for _, m := range l.Runs {
		if !m.Start.Before(tt.Day) && m.Start.Before(end) {
			inDay = append(inDay, m)
		}
	}

	dense := len(inDay) > planDense
	for _, m := range inDay {
		out.Marks = append(out.Marks, u.runMark(tt, m, y, dense, x))
	}
	out.Marks = append(out.Marks, u.plannedMarks(tt, l, y, end, x)...)

	for _, o := range l.Overdue {
		from, until := laterTime(o.Since, tt.Day), earlierTime(o.Until, end)
		if !until.After(from) {
			continue
		}

		out.Marks = append(out.Marks, planMark{
			Shape: shapeRect, X: x(from), Y: y - planOverdueH/2, W: x(until) - x(from), H: planOverdueH, Class: "ev is-late",
			Tip: u.T("plan.overdue_tip", "since", u.when(o.Since)+" "+zoneOf(tt.Zone).Name),
		})
		out.Notes = append(out.Notes, planNote{X: x(from) + planAxisPad, Y: y + planNameDrop*2, Text: u.T("plan.overdue"), Class: "is-late"})
	}

	if l.State == model.JobPaused {
		mid := x(tt.Day.Add(halfDay))
		out.Notes = append(out.Notes, planNote{X: mid, Y: y + planNameDrop*2, Text: u.T("plan.paused"), Mid: true})
	}
	return out
}

// halfDay is where a lane's single note sits.
const halfDay = 12 * time.Hour

// runMark draws one run: a circle for ok, a square for a failure, a bar
// while it runs, a thin line when the lane is dense.
func (u ui) runMark(tt status.Timetable, m status.Mark, y px, dense bool, x func(time.Time) px) planMark {
	end := m.End
	if end.IsZero() {
		end = tt.Now
	}

	mk := planMark{
		State: runKey(m.State), Class: "ev", Href: "/runs/" + strconv.FormatInt(m.ID, 10),
		Tip: u.tickTip(status.Tick{ID: m.ID, State: m.State, Start: m.Start, Length: end.Sub(m.Start)}),
	}

	switch {
	case dense:
		// ok a short line, anything else the full height: told apart by length.
		h := px(planDenseH)
		if m.State == model.RunOK {
			h = planDenseH * denseOKShare
		}
		mk.Shape, mk.X, mk.Y, mk.W, mk.H = shapeRect, x(m.Start)-planDenseW/2, y+planDenseH/2-h, planDenseW, h
	case m.State == model.RunRunning:
		mk.Shape, mk.X, mk.Y, mk.H = shapeRect, x(m.Start), y-planBar/2, planBar
		mk.W = px(math.Max(planMinRun, float64(x(end)-x(m.Start))))
	case m.State == model.RunOK:
		mk.Shape, mk.X, mk.Y, mk.R = shapeCircle, x(m.Start), y, planRun
	default:
		mk.Shape, mk.X, mk.Y, mk.W, mk.H = shapeRect, x(m.Start)-planRun, y-planRun, 2*planRun, 2*planRun
	}
	return mk
}

// plannedMarks are the slots still to come, hollow; a crowded lane gets
// one line from the first slot to the end of the day.
func (u ui) plannedMarks(tt status.Timetable, l status.Lane, y px, end time.Time, x func(time.Time) px) []planMark {
	var slots []time.Time
	for _, t := range l.Planned {
		if !t.Before(tt.Day) && t.Before(end) {
			slots = append(slots, t)
		}
	}

	zone := " " + zoneOf(tt.Zone).Name
	if l.Dense || len(slots) > planDense {
		from := laterTime(tt.Now, tt.Day)
		if len(slots) > 0 && !l.Dense {
			from = slots[0]
		}
		if !from.Before(end) {
			return nil
		}

		return []planMark{{
			Shape: shapeLine, X: x(from), X2: x(end), Y: y, Class: "ev is-plan",
			Tip: u.T("plan.dense_tip", "from", u.when(from)+zone),
		}}
	}

	var out []planMark
	for _, t := range slots {
		out = append(out, planMark{
			Shape: shapeCircle, X: x(t), Y: y, R: planSlot, Class: "ev is-plan",
			Tip: u.T("plan.planned") + " · " + u.when(t) + zone,
		})
	}
	return out
}

func laterTime(a, b time.Time) time.Time {
	if a.After(b) {
		return a
	}
	return b
}

func earlierTime(a, b time.Time) time.Time {
	if a.Before(b) {
		return a
	}
	return b
}

// Duration chart geometry: durCols columns of a 0..durHeight box.
const (
	durCols   = 30
	durHeight = 100
	durMinBar = 1.5
	// durMinCols keeps a job with few runs from drawing a few huge bars.
	durMinCols = 8
	durHeadro  = 1.15 // room above the tallest bar
	durBarGap  = .1   // space left of a bar inside its column
)

// durBar is one run on the duration chart.
type durBar struct {
	X     px
	Y, H  px
	State string
	Href  string
	Tip   string
}

// durView is the bars of a job's last runs with their value axis (O3).
type durView struct {
	N        int
	H        int
	Bars     []durBar
	Unit     string
	Axis     []string // top to bottom: the maximum, half of it, zero
	From, To string
	Watch    bool
}

// durations draws the length of each run, or for a watch the gap to the
// ping before. Empty when there is nothing to draw.
func (u ui) durations(p status.JobPage) durView {
	v := durView{N: durMinCols, H: durHeight, Bars: []durBar{}, Watch: p.Tile.Job.Kind == model.KindWatch}

	type point struct {
		d status.Duration
		v time.Duration
	}
	var pts []point
	for i, d := range p.Durations {
		if !v.Watch {
			pts = append(pts, point{d, d.Value})
			continue
		}
		if i > 0 {
			pts = append(pts, point{d, d.At.Sub(p.Durations[i-1].At)})
		}
	}
	if len(pts) > durCols {
		pts = pts[len(pts)-durCols:]
	}
	if len(pts) == 0 {
		return v
	}

	var top time.Duration
	for _, p := range pts {
		if p.v > top {
			top = p.v
		}
	}
	v.N = max(durMinCols, len(pts))
	unit, unitKey := scaleOf(top)
	ceiling := niceMax(float64(top) / float64(unit) * durHeadro)

	v.Unit = u.word("unit.", unitKey)
	v.Axis = []string{u.num(ceiling), u.num(ceiling / 2), "0"}
	v.From, v.To = u.when(pts[0].d.At), u.when(pts[len(pts)-1].d.At)

	x := 0
	for _, p := range pts {
		h := math.Max(durMinBar, durHeight*float64(p.v)/float64(unit)/ceiling)
		tip := u.tickTip(status.Tick{ID: p.d.RunID, State: p.d.State, Start: p.d.At, Length: p.d.Value})
		if v.Watch {
			tip = fmt.Sprintf("#%d · %s %s · %s", p.d.RunID, u.when(p.d.At), zoneOf(u.zone).Name, u.T("job.gap_tip", "gap", u.dur(p.v)))
		}
		v.Bars = append(v.Bars, durBar{
			X: px(x) + durBarGap, Y: px(durHeight - h), H: px(h), State: runKey(p.d.State),
			Href: "/runs/" + strconv.FormatInt(p.d.RunID, 10), Tip: tip,
		})
		x++
	}
	return v
}

// scaleOf picks the unit of a value axis: seconds, minutes or hours.
func scaleOf(top time.Duration) (time.Duration, string) {
	switch {
	case top < time.Second:
		return time.Millisecond, "ms"
	case top < secsShown:
		return time.Second, "s"
	case top < minsShown:
		return time.Minute, "min"
	}
	return time.Hour, "h"
}

// niceMax rounds up to one significant digit: 23 -> 30, 0.7 -> 0.8.
func niceMax(raw float64) float64 {
	if raw <= 0 {
		return 1
	}
	mag := math.Pow(10, math.Floor(math.Log10(raw)))
	return math.Ceil(raw/mag) * mag
}

// num writes an axis value with the language's decimal mark.
func (u ui) num(v float64) string {
	s := strconv.FormatFloat(v, 'f', -1, 64)
	if u.locale == i18n.LocaleDE {
		s = strings.Replace(s, ".", ",", 1)
	}
	return s
}
