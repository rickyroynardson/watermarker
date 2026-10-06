package database

import (
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestPoolConfig(t *testing.T) {
	// Explicit demo credentials keep tests independent of local password files.
	const url = "postgres://demo:demo@localhost:5432/demo?sslmode=disable"
	const dsn = "host=localhost port=5432 user=demo password=demo dbname=demo sslmode=disable"
	for _, connString := range []string{url, dsn} {
		c, err := poolConfig(connString)
		require.NoError(t, err)
		require.EqualValues(t, 25, c.MaxConns)
		require.EqualValues(t, 5, c.MinConns)
		require.Equal(t, 15*time.Minute, c.MaxConnIdleTime)
		require.Equal(t, time.Hour, c.MaxConnLifetime)
		require.Equal(t, time.Minute, c.HealthCheckPeriod)
	}
	options := []string{"pool_max_conns=4", "pool_min_conns=0", "pool_min_idle_conns=1", "pool_max_conn_idle_time=3m", "pool_max_conn_lifetime=20m", "pool_health_check_period=10s"}
	for _, connString := range []string{url + "&" + strings.Join(options, "&"), dsn + " " + strings.Join(options, " ")} {
		c, err := poolConfig(connString)
		require.NoError(t, err)
		require.EqualValues(t, 4, c.MaxConns)
		require.Zero(t, c.MinConns)
		require.EqualValues(t, 1, c.MinIdleConns)
		require.Equal(t, 3*time.Minute, c.MaxConnIdleTime)
		require.Equal(t, 20*time.Minute, c.MaxConnLifetime)
		require.Equal(t, 10*time.Second, c.HealthCheckPeriod)
		for name := range c.ConnConfig.RuntimeParams {
			require.False(t, strings.HasPrefix(name, "pool_"), "pool options must not reach PostgreSQL")
		}
	}
	c, err := poolConfig(url + "&pool_max_conns=10&application_name=pool-demo&statement_timeout=2000")
	require.NoError(t, err)
	require.EqualValues(t, 10, c.MaxConns)
	require.EqualValues(t, 5, c.MinConns, "omitted options retain application defaults")
	require.Equal(t, "pool-demo", c.ConnConfig.RuntimeParams["application_name"])
	require.Equal(t, "2000", c.ConnConfig.RuntimeParams["statement_timeout"])

	for _, options := range []string{
		"pool_max_conns=0", "pool_max_conns=nope", "pool_min_conns=-1",
		"pool_max_conns=4", // Omitted minimum retains 5, exceeding maximum.
		"pool_min_idle_conns=-1", "pool_min_idle_conns=26",
		"pool_health_check_period=0s", "pool_max_conn_idle_time=-1m",
		"pool_max_conn_lifetime=0s", "pool_max_conn_lifetime=invalid",
	} {
		t.Run(options, func(t *testing.T) {
			c, err := poolConfig(url + "&" + options)
			require.Error(t, err)
			require.Nil(t, c)
		})
	}
}
