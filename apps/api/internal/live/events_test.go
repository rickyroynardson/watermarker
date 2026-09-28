package live

import (
	"testing"

	"github.com/google/uuid"
)

func TestFanoutCoalescesAndUnsubscribes(t *testing.T) {
	e := &Events{viewers: make(map[uuid.UUID]map[chan struct{}]struct{})}
	id := uuid.New()
	a, cancelA := e.Subscribe(id)
	b, cancelB := e.Subscribe(id)
	defer cancelB()
	other, cancelOther := e.Subscribe(uuid.New())
	defer cancelOther()
	e.notify(id)
	e.notify(id)
	if len(a) != 1 || len(b) != 1 || len(other) != 0 {
		t.Fatal("incorrect routing or unbounded buffering")
	}
	<-a
	<-b
	cancelA()
	e.notify(uuid.Nil) // Redis reconnection refreshes every active viewer.
	if len(a) != 0 || len(b) != 1 || len(other) != 1 {
		t.Fatal("incorrect reconnect or unsubscribe fanout")
	}
}
