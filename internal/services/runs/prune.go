package runs

import (
	"database/sql"
	"time"

	"jidoka/internal/db"
	"jidoka/internal/model"
	"jidoka/internal/repos"
)

// Prune deletes runs past retention (J12): per job its keep or the
// global one, at most KeepRuns per job; runs of removed jobs after the
// global keep (J21).
func (s *Service) Prune(set model.Set, now time.Time) error {
	keep, keepRuns := set.Settings.Keep, set.Settings.KeepRuns
	live := make([]string, 0, len(set.Jobs))

	return db.WithTx(s.db, func(q *sql.Tx) error {
		for id, job := range set.Jobs {
			live = append(live, id)

			jobKeep := keep
			if job.Keep > 0 {
				jobKeep = job.Keep
			}
			if _, err := repos.PruneRuns(q, id, now.Add(-jobKeep), keepRuns); err != nil {
				return err
			}
		}

		_, err := repos.PruneGone(q, live, now.Add(-keep))
		return err
	})
}
