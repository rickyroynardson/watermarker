// Package live carries best-effort wake-up signals; PostgreSQL owns the actual state.
package live

import (
	"context"
	"fmt"
	"os"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/redis/go-redis/v9"
	"go.uber.org/zap"
)

const channel = "watermarker:batch_changes"

func FromEnv() (*Events, error) {
	url := os.Getenv("REDIS_URL")
	if url == "" {
		url = "redis://localhost:6379/0"
	}
	return New(url)
}

type Events struct {
	client  *redis.Client
	mu      sync.Mutex
	viewers map[uuid.UUID]map[chan struct{}]struct{}
}

func New(url string) (*Events, error) {
	opts, err := redis.ParseURL(url)
	if err != nil {
		return nil, err
	}
	opts.DialTimeout = time.Second
	opts.ReadTimeout = time.Second
	opts.WriteTimeout = time.Second
	opts.ContextTimeoutEnabled = true
	opts.MaxRetries = -1
	return &Events{client: redis.NewClient(opts), viewers: make(map[uuid.UUID]map[chan struct{}]struct{})}, nil
}

func (e *Events) Close() error { return e.client.Close() }

// Publish follows a successful commit. A Redis outage must not turn that commit into a failed job.
func (e *Events) Publish(ctx context.Context, id uuid.UUID) {
	if e == nil {
		return
	}
	// ponytail: post-commit publication can be lost; snapshots recover current state, use an outbox for guaranteed events.
	ctx, cancel := context.WithTimeout(ctx, time.Second)
	defer cancel()
	if err := e.client.Publish(ctx, channel, id.String()).Err(); err != nil {
		zap.L().Warn("publish batch change", zap.String("batch_id", id.String()), zap.Error(err))
	}
}

// Listen starts one subscriber for the entire API instance, not one per browser.
func (e *Events) Listen(ctx context.Context) (func(), error) {
	sub := e.client.Subscribe(ctx, channel)
	readyCtx, cancel := context.WithTimeout(ctx, 3*time.Second)
	_, err := sub.Receive(readyCtx)
	cancel()
	if err != nil {
		_ = sub.Close()
		return nil, err
	}
	ctx, stop := context.WithCancel(ctx)
	zap.L().Info("Redis subscriber connected", zap.String("channel", channel))
	messages := sub.ChannelWithSubscriptions()
	done := make(chan struct{})
	go func() {
		defer close(done)
		defer zap.L().Info("Redis subscriber stopped", zap.String("channel", channel))
		for {
			select {
			case <-ctx.Done():
				return
			case message, ok := <-messages:
				if !ok {
					return
				}
				switch message := message.(type) {
				case *redis.Message:
					id, err := uuid.Parse(message.Payload)
					if err == nil {
						e.notify(id)
					}
				case *redis.Subscription:
					zap.L().Info("Redis subscriber resubscribed", zap.String("channel", message.Channel))
					// A re-subscription means changes may have been missed: wake every viewer.
					e.notify(uuid.Nil)
				}
			}
		}
	}()
	return func() { stop(); _ = sub.Close(); <-done }, nil
}

func (e *Events) Subscribe(id uuid.UUID) (<-chan struct{}, func()) {
	updates := make(chan struct{}, 1)
	e.mu.Lock()
	if e.viewers[id] == nil {
		e.viewers[id] = make(map[chan struct{}]struct{})
	}
	e.viewers[id][updates] = struct{}{}
	e.logViewers("SSE viewer connected", id, updates)
	e.mu.Unlock()
	return updates, func() {
		e.mu.Lock()
		defer e.mu.Unlock()
		delete(e.viewers[id], updates)
		if len(e.viewers[id]) == 0 {
			delete(e.viewers, id)
		}
		e.logViewers("SSE viewer disconnected", id, updates)
	}
}

// Called with mu held so the logged map matches the registration/removal.
func (e *Events) logViewers(message string, id uuid.UUID, updates chan struct{}) {
	viewers := make(map[string][]string, len(e.viewers))
	for batchID, channels := range e.viewers {
		for ch := range channels {
			viewers[batchID.String()] = append(viewers[batchID.String()], fmt.Sprintf("%p", ch))
		}
	}
	zap.L().Info(message, zap.String("batch_id", id.String()), zap.String("viewer_id", fmt.Sprintf("%p", updates)),
		zap.Int("batch_viewers", len(e.viewers[id])), zap.Any("viewers", viewers))
}

func (e *Events) notify(id uuid.UUID) {
	e.mu.Lock()
	defer e.mu.Unlock()
	for batchID, viewers := range e.viewers {
		if id != uuid.Nil && batchID != id {
			continue
		}
		for updates := range viewers {
			// One pending wake-up is enough: viewers always fetch the latest snapshot.
			select {
			case updates <- struct{}{}:
				zap.L().Info("SSE viewer notified", zap.String("batch_id", batchID.String()), zap.String("viewer_id", fmt.Sprintf("%p", updates)))
			default:
				zap.L().Info("SSE notification coalesced", zap.String("batch_id", batchID.String()), zap.String("viewer_id", fmt.Sprintf("%p", updates)))
			}
		}
	}
}
