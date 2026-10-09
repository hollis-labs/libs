package sqlstore

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/hollis-labs/libs/util/scheduler"
)

// isPrunedOccurrence is called by CreateFire inside its write transaction.
func isPrunedOccurrence(ctx context.Context, tx *sql.Tx, c scheduler.FireCreation) (bool, error) {
	var through string
	err := tx.QueryRowContext(ctx, `SELECT through_at FROM gosched_pruned WHERE schedule_id=?`, c.ScheduleID).Scan(&through)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	cutoff, err := parseTime(through)
	if err != nil {
		return false, err
	}
	return !c.Fire.ScheduledAt.After(cutoff), nil
}

// Prune removes eligible terminal fires and advances compact permanent fences
// atomically. A write reservation is acquired before reading candidates so a
// competing claim/create cannot race deletion or fence insertion.
func (s *Store) Prune(ctx context.Context, olderThan time.Time) (int, error) {
	tx, done, err := s.begin(ctx)
	if err != nil {
		return 0, err
	}
	defer done()
	if _, err = tx.ExecContext(ctx, `UPDATE gosched_pruned SET through_at=through_at WHERE 0`); err != nil {
		return 0, err
	}
	schedules, err := tx.QueryContext(ctx, scheduleSelect)
	if err != nil {
		return 0, err
	}
	list, err := scanSchedules(schedules)
	if err != nil {
		return 0, err
	}
	keep := map[string]int{}
	for _, sch := range list {
		keep[sch.ID] = sch.KeepLastN
	}
	rows, err := tx.QueryContext(ctx, `SELECT id,schedule_id,scheduled_at,status FROM gosched_fires`)
	if err != nil {
		return 0, err
	}
	var fires []scheduler.Fire
	for rows.Next() {
		var f scheduler.Fire
		var at string
		if err = rows.Scan(&f.ID, &f.ScheduleID, &at, &f.Status); err != nil {
			_ = rows.Close()
			return 0, err
		}
		f.ScheduledAt, err = parseTime(at)
		if err != nil {
			_ = rows.Close()
			return 0, err
		}
		fires = append(fires, f)
	}
	err = rows.Err()
	_ = rows.Close()
	if err != nil {
		return 0, err
	}
	candidates := scheduler.PrunableFires(fires, keep, olderThan)
	for _, f := range candidates {
		if _, err = tx.ExecContext(ctx, `INSERT INTO gosched_pruned(schedule_id,through_at) VALUES(?,?) ON CONFLICT(schedule_id) DO UPDATE SET through_at=MAX(through_at,excluded.through_at)`, f.ScheduleID, formatTime(f.ScheduledAt)); err != nil {
			return 0, err
		}
		if _, err = tx.ExecContext(ctx, `DELETE FROM gosched_fire_options WHERE fire_id=?`, f.ID); err != nil {
			return 0, err
		}
		res, deleteErr := tx.ExecContext(ctx, `DELETE FROM gosched_fires WHERE id=? AND status IN ('succeeded','skipped','exhausted')`, f.ID)
		if deleteErr != nil {
			return 0, deleteErr
		}
		n, affectedErr := res.RowsAffected()
		if affectedErr != nil {
			return 0, affectedErr
		}
		if n != 1 {
			return 0, fmt.Errorf("sqlstore: prune candidate changed")
		}
	}
	if err = tx.Commit(); err != nil {
		return 0, err
	}
	return len(candidates), nil
}
