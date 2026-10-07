package database

import (
	"context"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

func ConnectPgx(ctx context.Context, url string) (*pgxpool.Pool, error) {
	c, err := poolConfig(url)
	if err != nil {
		return nil, err
	}

	dbpool, err := pgxpool.NewWithConfig(ctx, c)
	if err != nil {
		return nil, err
	}

	return dbpool, nil
}

func poolConfig(connString string) (*pgxpool.Config, error) {
	c, err := pgxpool.ParseConfig(connString)
	if err != nil {
		return nil, err
	}
	// pgxpool consumes pool_* runtime parameters while parsing. Read the
	// original options with pgconn's parser to preserve explicit settings in
	// both URL and keyword/value DSNs, without implementing another DSN parser.
	raw, err := pgconn.ParseConfig(connString)
	if err != nil {
		return nil, err
	}
	if _, set := raw.RuntimeParams["pool_max_conns"]; !set {
		c.MaxConns = 25
	}
	if _, set := raw.RuntimeParams["pool_min_conns"]; !set {
		c.MinConns = 5
	}
	if _, set := raw.RuntimeParams["pool_max_conn_idle_time"]; !set {
		c.MaxConnIdleTime = 15 * time.Minute
	}
	if _, set := raw.RuntimeParams["pool_max_conn_lifetime"]; !set {
		c.MaxConnLifetime = time.Hour
	}
	if _, set := raw.RuntimeParams["pool_health_check_period"]; !set {
		c.HealthCheckPeriod = time.Minute
	}
	if c.MinConns < 0 || c.MinConns > c.MaxConns {
		return nil, fmt.Errorf("pool_min_conns must be between 0 and pool_max_conns")
	}
	if c.MinIdleConns < 0 || c.MinIdleConns > c.MaxConns {
		return nil, fmt.Errorf("pool_min_idle_conns must be between 0 and pool_max_conns")
	}
	for name, value := range map[string]time.Duration{
		"pool_max_conn_idle_time":  c.MaxConnIdleTime,
		"pool_max_conn_lifetime":   c.MaxConnLifetime,
		"pool_health_check_period": c.HealthCheckPeriod,
	} {
		if value <= 0 {
			return nil, fmt.Errorf("%s must be greater than zero", name)
		}
	}
	return c, nil
}
