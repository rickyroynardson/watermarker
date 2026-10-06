package database

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5/pgxpool"
)

type Activity struct {
	Blocked, Blocking, IdleTransactions int64
	OldestTransactionSeconds            float64
}

// ObserveActivity samples client sessions in the current database, excluding
// this observation query. It reads no SQL text and never cancels other sessions.
func ObserveActivity(ctx context.Context, db *pgxpool.Pool) (Activity, error) {
	var activity Activity
	var visible bool
	err := db.QueryRow(ctx, `WITH sessions AS MATERIALIZED (
 SELECT pid,state,wait_event_type,xact_start
 FROM pg_stat_activity
 WHERE datname=current_database() AND pid<>pg_backend_pid()
   AND (backend_type='client backend' OR backend_type IS NULL)
), waiting AS MATERIALIZED (
 SELECT pid FROM sessions WHERE wait_event_type='Lock'
)
SELECT (SELECT count(*) FROM waiting),
 (SELECT count(DISTINCT blocker) FROM waiting,
   LATERAL unnest(pg_blocking_pids(pid)) AS blockers(blocker) WHERE blocker>0),
 count(*) FILTER (WHERE state IN ('idle in transaction','idle in transaction (aborted)')),
 COALESCE(GREATEST(extract(epoch FROM clock_timestamp()-min(xact_start)),0),0)::double precision,
 COALESCE(bool_and(state IS NOT NULL AND state<>'disabled'),true)
FROM sessions`).Scan(&activity.Blocked, &activity.Blocking, &activity.IdleTransactions, &activity.OldestTransactionSeconds, &visible)
	if err != nil {
		return Activity{}, err
	}
	if !visible {
		return Activity{}, fmt.Errorf("database activity is hidden or tracking is disabled; enable track_activities and provide monitor visibility into other roles, such as pg_read_all_stats")
	}
	return activity, nil
}
