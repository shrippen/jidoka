package settings

import (
	"sort"
	"strconv"
	"strings"

	"jidoka/internal/conf"
	"jidoka/internal/i18n"
	"jidoka/internal/model"
)

// draftOf is base with the fields of f; the profiles stay as they are.
// The errors are those of the text itself (a number that is none, a
// half-filled tag row); the values are checked by conf.
func (s *Service) draftOf(base conf.Draft, f Form) (conf.Draft, Errors) {
	errs := Errors{}
	d := conf.Draft{
		Zone:      strings.TrimSpace(f.Zone),
		Locale:    strings.TrimSpace(f.Locale),
		Heartbeat: strings.TrimSpace(f.Heartbeat),
		Keep:      strings.TrimSpace(f.Keep),
		AndonURL:  strings.TrimSpace(f.AndonURL),
		BaseURL:   strings.TrimSpace(f.BaseURL),
		Profiles:  base.Profiles,
	}
	if s.cfg.BaseURLEnv != "" {
		d.BaseURL = base.BaseURL
	}

	// Quiet hours are one value "22:00-07:00"; neither field means none.
	from, to := strings.TrimSpace(f.QuietFrom), strings.TrimSpace(f.QuietTo)
	if from != "" || to != "" {
		d.Quiet = from + "-" + to
	}

	d.MaxParallel = intOf(errs, "max_parallel", f.MaxParallel)
	d.KeepRuns = intOf(errs, "keep_runs", f.KeepRuns)

	d.Channels.Apprise = appriseOf(errs, f)
	d.Channels.Mail = mailOf(f)

	return d, errs
}

// intOf reads an optional whole number; empty is "not set".
func intOf(errs Errors, field, text string) *int {
	text = strings.TrimSpace(text)
	if text == "" {
		return nil
	}

	n, err := strconv.Atoi(text)
	if err != nil {
		errs[field] = fieldErr(field, keyInt)
		return nil
	}

	return &n
}

// appriseOf is the channel when the form says anything about it.
func appriseOf(errs Errors, f Form) *conf.DraftApprise {
	url := strings.TrimSpace(f.AppriseURL)
	tags := map[string]string{}

	for _, row := range f.Tags {
		name, tag := strings.TrimSpace(row.Name), strings.TrimSpace(row.Tag)
		if name == "" && tag == "" {
			continue
		}

		_, dup := tags[name]
		if name == "" || tag == "" || dup {
			errs[fieldTags] = fieldErr(fieldTags, keyTag)
			continue
		}
		tags[name] = tag
	}

	if url == "" && len(tags) == 0 {
		return nil
	}

	return &conf.DraftApprise{URL: url, Tags: tags}
}

// mailOf is the channel when the form says anything about it.
func mailOf(f Form) *conf.DraftMail {
	m := conf.DraftMail{
		SMTP: strings.TrimSpace(f.SMTP), Security: strings.TrimSpace(f.Security), User: strings.TrimSpace(f.User), From: strings.TrimSpace(f.From),
	}
	for _, line := range strings.Split(f.ToList, "\n") {
		if to := strings.TrimSpace(line); to != "" {
			m.To = append(m.To, to)
		}
	}

	if m.SMTP == "" && m.User == "" && m.From == "" && len(m.To) == 0 {
		return nil
	}

	return &m
}

func fieldErr(field, key string) model.LoadError {
	return fieldErrM(field, i18n.M(key))
}

func fieldErrM(field string, m i18n.Msg) model.LoadError {
	return model.LoadError{File: "settings", Field: field, Key: m.Key, Params: m.Params, Msg: m.Error()}
}

func intText(n *int) string {
	if n == nil {
		return ""
	}

	return strconv.Itoa(*n)
}

func sortTags(rows []TagRow) {
	sort.Slice(rows, func(a, b int) bool { return rows[a].Name < rows[b].Name })
}
