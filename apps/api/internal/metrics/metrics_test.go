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
	BatchCompleted(context.Background(), 12.5, true)
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
			} else if m.Name == "watermarker.batch.duration" {
				outcome, _ := attrs.Value("outcome")
				if outcome.AsString() != "failed" || attrs.Len() != 1 || h.DataPoints[0].Sum != 12.5 {
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
	if seen != 3 {
		t.Fatalf("got %d metrics", seen)
	}

	QueueDepth(context.Background(), "jobs", [3]int64{7, 2, 1})
	OutboxBacklog(context.Background(), 2, 120)
	BacklogObservation(context.Background(), "jobs", nil)
	BacklogObservation(context.Background(), "outbox", errors.New("database unavailable"))
	data = metricdata.ResourceMetrics{}
	if err := reader.Collect(context.Background(), &data); err != nil {
		t.Fatal(err)
	}
	gauges := 0
	for _, scope := range data.ScopeMetrics {
		for _, m := range scope.Metrics {
			switch m.Name {
			case "watermarker.queue.depth":
				points := m.Data.(metricdata.Gauge[int64]).DataPoints
				if len(points) != 3 {
					t.Fatal(points)
				}
				for _, point := range points {
					state, _ := point.Attributes.Value("state")
					if point.Value != map[string]int64{"visible": 7, "in_flight": 2, "delayed": 1}[state.AsString()] {
						t.Fatal(point)
					}
				}
				gauges++
			case "watermarker.outbox.pending":
				if m.Data.(metricdata.Gauge[int64]).DataPoints[0].Value != 2 {
					t.Fatal(m)
				}
				gauges++
			case "watermarker.outbox.oldest.age":
				if m.Data.(metricdata.Gauge[float64]).DataPoints[0].Value != 120 {
					t.Fatal(m)
				}
				gauges++
			case "watermarker.backlog.observation.success":
				points := m.Data.(metricdata.Gauge[int64]).DataPoints
				if len(points) != 2 {
					t.Fatal(points)
				}
				for _, point := range points {
					source, _ := point.Attributes.Value("source")
					expected := int64(1)
					if source.AsString() == "outbox" {
						expected = 0
					}
					if point.Value != expected {
						t.Fatal(point)
					}
				}
				gauges++
			case "watermarker.backlog.observation.time":
				for _, point := range m.Data.(metricdata.Gauge[int64]).DataPoints {
					if time.Now().Unix()-point.Value > 5 {
						t.Fatal(point)
					}
				}
				gauges++
			}
		}
	}
	if gauges != 5 {
		t.Fatalf("got %d backlog gauges", gauges)
	}
}
