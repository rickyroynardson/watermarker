# Learning database pool configuration and connection budgeting

This stays within the database performance topic. The earlier metrics show pool
pressure; this change lets us tune the pool through its existing connection
string settings. No database migration or additional configuration service is needed.

## The bug and fix

Previously, `ConnectPgx` parsed DATABASE_URL and then unconditionally replaced
MaxConns, MinConns, idle lifetime, maximum lifetime, and health-check interval.
Even a valid `pool_max_conns=10` option was overwritten with 25.

The shared helper now preserves explicit pgx options. Only omitted settings get
our existing defaults. pgx still parses the URL/DSN and numeric/duration values;
we do not implement a custom connection-string parser. Because pgxpool removes
its `pool_*` options from runtime parameters, the helper also uses pgconn's parser
to determine which options were supplied before that removal. This happens at
pool creation, not per request. Pool options are not sent as server parameters.

The API, consumer, monitor, and cleanup already use this helper, so they all
receive the fix. The Python worker does not connect to PostgreSQL.

## Defaults and supported overrides

| DATABASE_URL option | Application default when omitted |
| --- | --- |
| `pool_max_conns` | 25 |
| `pool_min_conns` | 5 |
| `pool_max_conn_idle_time` | 15m |
| `pool_max_conn_lifetime` | 1h |
| `pool_health_check_period` | 1m |

Other options understood by the installed pgxpool version still pass through
its parser, including `pool_min_idle_conns`, which defaults to zero. Minimum
connections and minimum idle connections are different settings: a pool with
five acquired connections can meet a total minimum of five while having no
idle connections. See the installed dependency's
[pgxpool configuration documentation](https://pkg.go.dev/github.com/jackc/pgx/v5@v5.10.0/pgxpool#ParseConfig).

We validate both minima as nonnegative and no greater than the maximum. The
three duration options above must be positive. pgx rejects malformed numbers,
malformed durations, and a nonpositive maximum. Invalid configuration returns a
startup error before constructing a pool, rather than silently reverting or
allowing an invalid health-check ticker.

If you set a maximum below five, also set the minimum explicitly. The omitted
minimum remains five and would otherwise exceed that maximum. An explicit
minimum of zero is valid; it is not treated as an omitted value.

## Configure with the existing DATABASE_URL

URL form, using demo credentials only:

```sh
export DATABASE_URL='postgres://demo:demo@localhost:5432/demo?sslmode=disable&pool_max_conns=10&pool_min_conns=2&pool_max_conn_idle_time=5m&pool_max_conn_lifetime=30m&pool_health_check_period=15s'
```

Keyword/value form is also supported:

```sh
export DATABASE_URL='host=localhost port=5432 dbname=demo user=demo password=demo sslmode=disable pool_max_conns=10 pool_min_conns=2 pool_max_conn_idle_time=5m pool_max_conn_lifetime=30m pool_health_check_period=15s'
```

Adapt your existing connection string and credentials. A URL uses `?` before
its first query parameter and `&` between subsequent parameters. Use your
deployment's TLS settings; these examples are for a local demo.

Restart the affected process after changing configuration. Each process uses
the DATABASE_URL it actually receives. With the same URL shared among all Go
processes, they receive the same limits but create separate pools. To budget
them differently, provide different pool options in each process's environment
or secret-backed connection string. No new environment variable names are added.

Standard PostgreSQL runtime options such as `application_name` and
`statement_timeout` remain intact. Pool configuration does not itself establish
a request deadline, cap S3/SQS calls, or change transaction locking.

## Count connections across processes

Suppose a deployment has:

| Process | Replicas | Maximum per pool | Potential connections |
| --- | ---: | ---: | ---: |
| API | 3 | 10 | 30 |
| Consumer | 1 | 5 | 5 |
| Monitor | 1 | 2 | 2 |
| Cleanup while running | 1 | 2 | 2 |
| Total for these processes | | | 39 |

This is an illustrative budget, not a recommendation for these exact values.
Also allow capacity for migrations, administrators, other applications,
PostgreSQL reserved connections, and overlap during rolling deployments.
Inspect the database configuration with `SHOW max_connections;` and check which
connections your application role can use. A pool maximum is a ceiling, not a
promise that every connection is currently open.

More connections can increase database contention rather than throughput.
Start from the observed pool utilization, successful wait time, cancellations,
database lock counts, and transaction age. Compare a modest configuration change
under the same workload before accepting it. A hot account row remains serialized
even when more connections wait on it.

## Verify

```sh
cd apps/api
# Parsing and validation, without Docker or a database:
go test -short ./internal/database -run '^TestPoolConfig$' -count=1
# Real helper and activity observation against disposable PostgreSQL:
go test ./internal/database -run '^TestObserveActivity$' -count=1 -timeout=3m
```

The configuration test checks existing defaults, URL and keyword/value overrides,
explicit zero minimum, partially supplied options, preserved server parameters,
invalid values, and removal of pool options from server runtime settings. The
activity integration test opens a real one-connection pool through ConnectPgx
with URL options and verifies the configured maximum and minimum before exercising
locking and monitoring permissions. It does not use the development database.

Existing URLs with no pool options retain their previous behavior. Explicit pool
options that used to be ignored now take effect or fail validation; review any
such options before restarting a deployment.

Read alongside [pool observability](database-pool-observability.md) and
[live database activity monitoring](database-activity-observability.md).
