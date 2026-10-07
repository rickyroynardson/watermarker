package database

import (
	"context"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/stretchr/testify/require"
	"github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/modules/postgres"
)

func TestObserveActivity(t *testing.T) {
	if testing.Short() {
		t.Skip("requires Docker")
	}
	if os.Getenv("DOCKER_HOST") == "" {
		host, err := exec.CommandContext(t.Context(), "docker", "context", "inspect", "--format", "{{.Endpoints.docker.Host}}").Output()
		require.NoError(t, err)
		t.Setenv("DOCKER_HOST", strings.TrimSpace(string(host)))
	}
	ctx, cancel := context.WithTimeout(t.Context(), time.Minute)
	defer cancel()
	pg, err := postgres.Run(ctx, "postgres:18-alpine", postgres.BasicWaitStrategies())
	testcontainers.CleanupContainer(t, pg)
	require.NoError(t, err)
	dsn, err := pg.ConnectionString(ctx, "sslmode=disable")
	require.NoError(t, err)
	// The generated DSN is a URL with sslmode already in its query string.
	// Exercise the shared helper and keep SET ROLE on its only connection.
	db, err := ConnectPgx(ctx, dsn+"&pool_max_conns=1&pool_min_conns=0")
	require.NoError(t, err)
	defer db.Close()
	require.EqualValues(t, 1, db.Stat().MaxConns())
	require.Zero(t, db.Config().MinConns)
	_, err = db.Exec(ctx, "CREATE TABLE activity_lab(id int PRIMARY KEY); INSERT INTO activity_lab VALUES(1)")
	require.NoError(t, err)
	holder, err := pgx.Connect(ctx, dsn)
	require.NoError(t, err)
	defer holder.Close(context.Background())
	waiter, err := pgx.Connect(ctx, dsn)
	require.NoError(t, err)
	defer waiter.Close(context.Background())
	tx, err := holder.Begin(ctx)
	require.NoError(t, err)
	defer tx.Rollback(context.Background())
	_, err = tx.Exec(ctx, "SELECT id FROM activity_lab FOR UPDATE")
	require.NoError(t, err)

	waitCtx, cancelWait := context.WithTimeout(ctx, 10*time.Second)
	done := make(chan error, 1)
	go func() {
		var id int
		done <- waiter.QueryRow(waitCtx, "SELECT id FROM activity_lab FOR UPDATE").Scan(&id)
	}()
	exited := false
	defer func() {
		cancelWait()
		if !exited {
			<-done
		}
	}()
	require.Eventually(t, func() bool {
		activity, err := ObserveActivity(ctx, db)
		return err == nil && activity.Blocked == 1 && activity.Blocking == 1 &&
			activity.IdleTransactions == 1 && activity.OldestTransactionSeconds > 0
	}, 3*time.Second, 25*time.Millisecond, "must observe the idle holder and blocked waiter")
	require.NoError(t, tx.Rollback(ctx))
	require.NoError(t, <-done)
	exited = true
	activity, err := ObserveActivity(ctx, db)
	require.NoError(t, err)
	require.Equal(t, Activity{}, activity, "observer's own transaction is excluded")

	_, err = db.Exec(ctx, "CREATE ROLE activity_reader; SET ROLE activity_reader")
	require.NoError(t, err)
	_, err = ObserveActivity(ctx, db)
	require.ErrorContains(t, err, "activity is hidden", "restricted visibility must not report healthy zeroes")
	_, err = db.Exec(ctx, "RESET ROLE; GRANT pg_read_all_stats TO activity_reader; SET ROLE activity_reader")
	require.NoError(t, err)
	activity, err = ObserveActivity(ctx, db)
	require.NoError(t, err)
	require.Equal(t, Activity{}, activity)
	_, err = holder.Exec(ctx, "SET track_activities=off")
	require.NoError(t, err)
	_, err = ObserveActivity(ctx, db)
	require.ErrorContains(t, err, "tracking is disabled")
	_, err = holder.Exec(ctx, "SET track_activities=on")
	require.NoError(t, err)
	_, err = ObserveActivity(ctx, db)
	require.NoError(t, err)
}
