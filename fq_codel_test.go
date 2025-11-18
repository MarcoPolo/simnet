//go:build go1.25

package simnet

import (
	"net"
	"testing"
	"testing/synctest"
	"time"
)

func TestFqCoDelSingleFlow(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		const (
			target    = 5 * time.Millisecond
			interval  = 100 * time.Millisecond
			quantum   = 128
			flowCount = 32
			payload   = 64
		)

		q := newTestFqCoDel(target, interval, quantum, flowCount)
		flow := mustNewTestFlow(t, q, 1, nil)

		for seq := range 3 {
			q.Enqueue(flow.packet(seq, payload))
		}

		for seq := range 3 {
			pkt, ok := q.Dequeue()
			if !ok {
				t.Fatalf("dequeue %d returned empty packet", seq)
			}
			flowID, gotSeq := decodeFlowAndSeq(pkt)
			if flowID != flow.id || gotSeq != seq {
				t.Fatalf("got flow %d seq %d, want flow %d seq %d", flowID, gotSeq, flow.id, seq)
			}
		}

		if pkt, ok := q.Dequeue(); ok {
			flowID, seq := decodeFlowAndSeq(pkt)
			t.Fatalf("queue should be empty, got flow %d seq %d", flowID, seq)
		}
	})
}

func TestFqCoDelMultipleFlowsNoStandingQueue(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		const (
			target    = 5 * time.Millisecond
			interval  = 100 * time.Millisecond
			quantum   = 64
			flowCount = 64
			payload   = 96
		)

		q := newTestFqCoDel(target, interval, quantum, flowCount)
		used := make(map[int]struct{})
		flows := []testFlow{
			mustNewTestFlow(t, q, 1, used),
			mustNewTestFlow(t, q, 2, used),
		}

		// Each flow only has a single packet in flight (no standing queue) and
		// packets should be dequeued in arrival order.
		for _, f := range flows {
			q.Enqueue(f.packet(0, payload))
		}

		for i, f := range flows {
			pkt, ok := q.Dequeue()
			if !ok {
				t.Fatalf("unexpected empty dequeue at position %d", i)
			}
			flowID, seq := decodeFlowAndSeq(pkt)
			if flowID != f.id || seq != 0 {
				t.Fatalf("got flow %d seq %d, want flow %d seq 0", flowID, seq, f.id)
			}
		}

		if pkt, ok := q.Dequeue(); ok {
			flowID, seq := decodeFlowAndSeq(pkt)
			t.Fatalf("queue should be empty after draining, got flow %d seq %d", flowID, seq)
		}
	})
}

func TestFqCoDelPersistentBadQueueIsIsolated(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		const (
			target    = 1 * time.Millisecond
			interval  = 20 * time.Millisecond
			quantum   = 64
			flowCount = 128
			payload   = 192
		)

		q := newTestFqCoDel(target, interval, quantum, flowCount)
		used := make(map[int]struct{})
		badFlow := mustNewTestFlow(t, q, 1, used)
		goodFlow := mustNewTestFlow(t, q, 2, used)

		// Build a persistently full queue for the bad flow so CoDel enters drop state.
		for seq := 0; seq < 10; seq++ {
			q.Enqueue(badFlow.packet(seq, payload))
		}

		badQueue := flowQueue(t, q, badFlow)
		if badQueue == nil {
			t.Fatal("bad flow queue missing")
		}

		time.Sleep(3 * interval)

		enteredDrop := false
		for attempt := 0; attempt < 6; attempt++ {
			pkt, ok := q.Dequeue()
			if !ok {
				t.Fatalf("bad flow should still have packets on attempt %d", attempt)
			}
			flowID, _ := decodeFlowAndSeq(pkt)
			if flowID != badFlow.id {
				t.Fatalf("unexpected flow %d while draining bad queue", flowID)
			}
			if badQueue.dropping {
				enteredDrop = true
				break
			}
			time.Sleep(interval)
		}
		if !enteredDrop {
			t.Fatal("persistently bad flow never entered drop state")
		}

		q.Enqueue(goodFlow.packet(0, payload))

		next, ok := q.Dequeue()
		if !ok {
			t.Fatal("expected good flow packet")
		}
		nextFlow, seq := decodeFlowAndSeq(next)
		if nextFlow != goodFlow.id || seq != 0 {
			t.Fatalf("expected good flow packet 0, got flow %d seq %d", nextFlow, seq)
		}
	})
}

func TestFqCoDelThreeFlowsNoStarvation(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		const (
			target    = 1 * time.Millisecond
			interval  = 20 * time.Millisecond
			quantum   = 64
			flowCount = 128
			payload   = 160
			rounds    = 3
		)

		q := newTestFqCoDel(target, interval, quantum, flowCount)
		used := make(map[int]struct{})
		flows := []testFlow{
			mustNewTestFlow(t, q, 1, used),
			mustNewTestFlow(t, q, 2, used),
			mustNewTestFlow(t, q, 3, used),
		}

		for seq := range rounds {
			for _, f := range flows {
				q.Enqueue(f.packet(seq, payload))
			}
		}

		for seq := range rounds {
			seen := make(map[int]bool)
			for range flows {
				pkt, ok := q.Dequeue()
				if !ok {
					t.Fatalf("unexpected empty dequeue while draining seq %d", seq)
				}
				flowID, gotSeq := decodeFlowAndSeq(pkt)
				if gotSeq != seq {
					t.Fatalf("flow %d got seq %d, want %d", flowID, gotSeq, seq)
				}
				seen[flowID] = true
			}
			if len(seen) != len(flows) {
				t.Fatalf("not all flows serviced for seq %d, got %v", seq, seen)
			}
		}

		if pkt, ok := q.Dequeue(); ok {
			flowID, seq := decodeFlowAndSeq(pkt)
			t.Fatalf("queue should be empty after all rounds, got flow %d seq %d", flowID, seq)
		}
	})
}

func newTestFqCoDel(target, interval time.Duration, quantum, flowCount int) *fqCoDel {
	q := newFqCoDel(target, interval, quantum, flowCount)
	return &q
}

type testFlow struct {
	id   int
	to   net.UDPAddr
	from net.UDPAddr
}

func (f testFlow) packet(seq, size int) Packet {
	to := f.to
	from := f.from
	return Packet{
		To:   &to,
		From: &from,
		buf:  flowPayload(f.id, seq, size),
	}
}

func (f testFlow) bucket(q *fqCoDel) int {
	to := f.to
	from := f.from
	probe := Packet{To: &to, From: &from}
	return int(probe.Hash(&q.hash) % uint64(len(q.flows)))
}

func decodeFlowAndSeq(p Packet) (flowID, seq int) {
	if len(p.buf) < 2 {
		return -1, -1
	}
	return int(p.buf[0]), int(p.buf[1])
}

func flowPayload(flowID, seq, size int) []byte {
	if size < 2 {
		size = 2
	}
	buf := make([]byte, size)
	buf[0] = byte(flowID)
	buf[1] = byte(seq)
	for i := 2; i < len(buf); i++ {
		buf[i] = byte(flowID + seq)
	}
	return buf
}

func flowQueue(t *testing.T, q *fqCoDel, f testFlow) *coDelQueueWithCredits {
	t.Helper()
	bucket := f.bucket(q)
	if bucket < 0 || bucket >= len(q.flows) {
		t.Fatalf("flow bucket %d out of range", bucket)
	}
	return q.flows[bucket]
}

func mustNewTestFlow(t *testing.T, q *fqCoDel, id int, usedBuckets map[int]struct{}) testFlow {
	t.Helper()

	for salt := range 512 {
		to := net.UDPAddr{
			IP:   net.IPv4(10, byte(id), byte(salt), byte(id+salt)),
			Port: 10000 + id + salt*7,
		}
		from := net.UDPAddr{
			IP:   net.IPv4(192, 0, byte(id+salt), byte(salt)),
			Port: 20000 + id + salt*11,
		}
		flow := testFlow{
			id:   id,
			to:   to,
			from: from,
		}
		bucket := flow.bucket(q)
		if usedBuckets != nil {
			if _, exists := usedBuckets[bucket]; exists {
				continue
			}
			usedBuckets[bucket] = struct{}{}
		}
		return flow
	}

	t.Fatalf("unable to derive flow %d with unique bucket", id)
	return testFlow{}
}
