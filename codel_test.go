//go:build go1.25

package simnet

import (
	"testing"
	"testing/synctest"
	"time"
)

func TestCodelQueueDropsPersistentBadQueue(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		const (
			target   = 5 * time.Millisecond
			interval = 100 * time.Millisecond
		)
		q := newCoDelQueue(target, interval)
		q.MTU = 1

		// Build a queue that stays full for more than one interval.
		for i := range 4 {
			q.Enqueue(Packet{buf: []byte{byte(i)}})
		}

		// Allow in-flight packets to accumulate sojourn time.
		time.Sleep(3 * q.interval)

		pkt, ok := q.Dequeue()
		if !ok || len(pkt.buf) == 0 {
			t.Fatal("expected packet before CoDel enters drop state")
		}
		if got := pkt.buf[0]; got != 0 {
			t.Fatalf("first dequeue returned %d, want 0", got)
		}
		if q.dropping {
			t.Fatal("queue should not drop until delay persists beyond interval")
		}

		// Keep the queue persistently bad so dropping kicks in.
		time.Sleep(3 * q.interval)

		pkt, ok = q.Dequeue()
		if !ok || len(pkt.buf) == 0 {
			t.Fatal("expected packet after CoDel begins dropping")
		}
		if got := pkt.buf[0]; got != 2 {
			t.Fatalf("persistent queue should drop packet 1, got %d", got)
		}
		if !q.dropping {
			t.Fatal("persistent bad queue should enter drop state")
		}
	})
}

func TestCodelQueueNoDropOnTransientQueue(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		const (
			target   = 5 * time.Millisecond
			interval = 100 * time.Millisecond
		)
		q := newCoDelQueue(target, interval)
		q.MTU = 1

		for i := range 2 {
			q.Enqueue(Packet{buf: []byte{byte(i)}})
		}

		// The queue goes above target but drains in less than one interval.
		time.Sleep(q.target + q.interval/10)

		pkt, ok := q.Dequeue()
		if !ok || len(pkt.buf) == 0 {
			t.Fatal("expected first packet delivery")
		}
		if got := pkt.buf[0]; got != 0 {
			t.Fatalf("expected packet 0, got %d", got)
		}

		time.Sleep(q.interval / 2)

		pkt, ok = q.Dequeue()
		if !ok || len(pkt.buf) == 0 {
			t.Fatal("expected second packet delivery")
		}
		if got := pkt.buf[0]; got != 1 {
			t.Fatalf("expected packet 1, got %d", got)
		}
		if q.dropping {
			t.Fatal("transient queue should not enter drop state")
		}
	})
}

func BenchmarkCodelQueueEnqueueDequeue(b *testing.B) {
	const initSize = 30000
	const queueSize = 50000

	packets := make([]Packet, queueSize)
	data := []byte("test")
	for i := range queueSize {
		packets[i] = Packet{buf: data}
	}

	q := newCoDelQueue(5*time.Millisecond, 100*time.Millisecond)
	for _, p := range packets {
		q.Enqueue(p)
	}

	i := 0
	for b.Loop() {
		q.Enqueue(packets[i])
		i = (i + 1) % queueSize

		pkt, ok := q.Dequeue()
		if !ok || len(pkt.buf) == 0 {
			b.Fatal("unexpected empty dequeue")
		}
	}
}
