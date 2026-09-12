package metrics

import (
	"context"
	"errors"
	"testing"
	"time"

	"go.opentelemetry.io/otel"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/metric/metricdata"
)

func TestMeasurements(t *testing.T) {
	reader := sdkmetric.NewManualReader()
	provider := sdkmetric.NewMeterProvider(sdkmetric.WithReader(reader))
	otel.SetMeterProvider(provider)
	defer provider.Shutdown(context.Background())
	HTTP(context.Background(), "arbitrary-method", "", 404, time.Second)
	Message(context.Background(), "process_result", time.Now(), errors.New("private error"))
	var data metricdata.ResourceMetrics
	if err := reader.Collect(context.Background(), &data); err != nil {
		t.Fatal(err)
	}
	seen := 0
	for _, scope := range data.ScopeMetrics {
		for _, m := range scope.Metrics {
			h := m.Data.(metricdata.Histogram[float64])
			if len(h.DataPoints) != 1 || h.DataPoints[0].Count != 1 {
				t.Fatalf("unexpected points: %+v", h)
			}
			attrs := h.DataPoints[0].Attributes
			if m.Name == "watermarker.http.request.duration" {
				method, _ := attrs.Value("method")
				route, _ := attrs.Value("route")
				if method.AsString() != "_OTHER" || route.AsString() != "unmatched" || h.DataPoints[0].Sum != 1 {
					t.Fatal(m)
				}
			} else {
				outcome, _ := attrs.Value("outcome")
				if outcome.AsString() != "error" || attrs.Len() != 2 {
					t.Fatal(m)
				}
			}
			seen++
		}
	}
	if seen != 2 {
		t.Fatalf("got %d metrics", seen)
	}
}
