package metrics

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/require"
	"github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/modules/postgres"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/metric/metricdata"
)

func TestDatabasePoolMetrics(t *testing.T) {
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
	config, err := pgxpool.ParseConfig(dsn)
	require.NoError(t, err)
	config.MaxConns = 1
	db, err := pgxpool.NewWithConfig(ctx, config)
	require.NoError(t, err)
	defer db.Close()

	reader := sdkmetric.NewManualReader()
	provider := sdkmetric.NewMeterProvider(sdkmetric.WithReader(reader))
	defer provider.Shutdown(context.Background())
	// Use a local provider so the test does not bind package-global instruments
	// to a provider that is shut down before the existing measurements test.
	stop := databasePool(provider.Meter("watermarker"), db)
	defer stop()
	conn, err := db.Acquire(ctx)
	require.NoError(t, err)
	defer conn.Release()
	acquireCtx, cancelAcquire := context.WithTimeout(ctx, 50*time.Millisecond)
	defer cancelAcquire()
	_, err = db.Acquire(acquireCtx)
	require.True(t, errors.Is(err, context.DeadlineExceeded), "%v", err)

	collect := func() map[string]float64 {
		var data metricdata.ResourceMetrics
		require.NoError(t, reader.Collect(ctx, &data), "collection must work even with every connection held")
		values := map[string]float64{}
		for _, scope := range data.ScopeMetrics {
			for _, m := range scope.Metrics {
				switch points := m.Data.(type) {
				case metricdata.Gauge[int64]:
					for _, point := range points.DataPoints {
						state, _ := point.Attributes.Value("state")
						values[m.Name+":"+state.AsString()] = float64(point.Value)
					}
				case metricdata.Sum[int64]:
					require.True(t, points.IsMonotonic)
					for _, point := range points.DataPoints {
						values[m.Name] = float64(point.Value)
					}
				case metricdata.Sum[float64]:
					require.True(t, points.IsMonotonic)
					for _, point := range points.DataPoints {
						values[m.Name] = point.Value
					}
				}
			}
		}
		return values
	}
	values := collect()
	require.Len(t, values, 8)
	require.Equal(t, float64(1), values["watermarker.db.pool.connections:acquired"])
	require.Zero(t, values["watermarker.db.pool.connections:idle"])
	require.Zero(t, values["watermarker.db.pool.connections:constructing"])
	require.Equal(t, float64(1), values["watermarker.db.pool.limit:"])
	require.Equal(t, float64(1), values["watermarker.db.pool.acquires"])
	require.Equal(t, float64(1), values["watermarker.db.pool.canceled"])
	require.GreaterOrEqual(t, values["watermarker.db.pool.waits"], float64(1), "first acquire waits for construction")
	require.Positive(t, values["watermarker.db.pool.wait.duration"])
	conn.Release()
	values = collect()
	require.Zero(t, values["watermarker.db.pool.connections:acquired"])
	require.Equal(t, float64(1), values["watermarker.db.pool.connections:idle"])
	stop()
	require.Empty(t, collect(), "unregistered pool must stop reporting")
}
