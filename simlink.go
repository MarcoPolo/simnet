package simnet

import (
	"context"
	"math"
	"net"
	"sync"
	"time"

	"golang.org/x/time/rate"
)

const Mibps = 1_000_000

// Creates a new RateLimiter with the following parameters:
// bandwidth (in bits/sec).
// burstSize is in Bytes
func newRateLimiter(bandwidth int, burstSize int) *rate.Limiter {
	// Convert bandwidth from bits/sec to bytes/sec
	bytesPerSecond := rate.Limit(float64(bandwidth) / 8.0)
	return rate.NewLimiter(bytesPerSecond, burstSize)
}

// packetWithDeliveryTime holds a packet along with its delivery time and enqueue time
type packetWithDeliveryTime struct {
	Packet
	DeliveryTime time.Time
}

// codelQueue is a FIFO queue with CoDel bufferbloat control
type codelQueue struct {
	mu        sync.Mutex
	packets   []*packetWithDeliveryTime
	newPacket chan struct{}
	closed    bool

	// CoDel state
	dropping     bool
	firstAbove   time.Time
	dropNext     time.Time
	count        int
	target       time.Duration // target queue delay (e.g., 5ms)
	interval     time.Duration // interval for sustained bad queue (e.g., 100ms)
	lastDropTime time.Time
}

func newCodelQueue(target, interval time.Duration) *codelQueue {
	q := &codelQueue{
		target:    target,
		interval:  interval,
		newPacket: make(chan struct{}, 1),
	}
	return q
}

// Enqueue adds a packet to the queue
func (q *codelQueue) Enqueue(p *packetWithDeliveryTime) {
	q.mu.Lock()
	defer q.mu.Unlock()
	if q.closed {
		return
	}
	q.packets = append(q.packets, p)

	// Signal that a new packet arrived (non-blocking)
	select {
	case q.newPacket <- struct{}{}:
	default:
	}
}

// Dequeue removes and returns the next packet when it's ready for delivery
// This blocks until a packet is available AND its delivery time has been reached
// Uses a timer that can be reset if a packet with earlier delivery time arrives
func (q *codelQueue) Dequeue() (*packetWithDeliveryTime, bool) {
	timer := time.NewTimer(time.Hour)
	timer.Stop()

	for {
		q.mu.Lock()

		if q.closed {
			q.mu.Unlock()
			timer.Stop()
			return nil, false
		}

		if len(q.packets) == 0 {
			// No packets, wait for one to arrive
			q.mu.Unlock()
			select {
			case <-q.newPacket:
				continue
			case <-timer.C:
				continue
			}
		}

		// Find packet with earliest delivery time
		earliestIdx := 0
		earliestTime := q.packets[0].DeliveryTime
		for i := 1; i < len(q.packets); i++ {
			if q.packets[i].DeliveryTime.Before(earliestTime) {
				earliestIdx = i
				earliestTime = q.packets[i].DeliveryTime
			}
		}

		now := time.Now()
		if now.Before(earliestTime) {
			// Not ready yet, wait until delivery time or new packet
			waitDuration := earliestTime.Sub(now)
			timer.Reset(waitDuration)
			q.mu.Unlock()

			select {
			case <-timer.C:
				// Timer expired, check again
				continue
			case <-q.newPacket:
				// New packet arrived, might have earlier delivery time
				timer.Stop()
				continue
			}
		}

		// Packet is ready, remove from queue and return it
		p := q.packets[earliestIdx]
		q.packets = append(q.packets[:earliestIdx], q.packets[earliestIdx+1:]...)

		// Reset CoDel state when queue becomes empty
		if len(q.packets) == 0 {
			q.dropping = false
			q.firstAbove = time.Time{}
		}

		q.mu.Unlock()

		return p, true
	}
}

// shouldDrop implements the CoDel dropping decision (thread-safe version)
func (q *codelQueue) shouldDrop(sojournTime time.Duration) bool {
	q.mu.Lock()
	defer q.mu.Unlock()
	return q.codelShouldDrop(sojournTime, time.Now())
}

// codelShouldDrop implements the CoDel dropping decision
func (q *codelQueue) codelShouldDrop(sojournTime time.Duration, now time.Time) bool {
	// Reset CoDel state when queue is empty (checked by caller before dequeue)
	// This is handled by resetting state when queue becomes empty in Dequeue

	if sojournTime < q.target {
		// Queue is good, reset state
		q.firstAbove = time.Time{}
		q.dropping = false
		return false
	}

	// Queue delay is above target
	if q.firstAbove.IsZero() {
		// First time above target, start tracking
		q.firstAbove = now.Add(q.interval)
		return false
	}

	if now.Before(q.firstAbove) {
		// Haven't been above target for long enough
		return false
	}

	// We've been above target for the full interval
	if !q.dropping {
		// Enter dropping state
		q.dropping = true
		q.count = 1
		q.dropNext = now
		q.lastDropTime = now
		return true
	}

	// Already in dropping state
	if now.After(q.dropNext) {
		// Time to drop another packet
		q.count++
		// Calculate next drop time using control law: interval / sqrt(count)
		delta := time.Duration(float64(q.interval) / math.Sqrt(float64(q.count)))
		q.dropNext = now.Add(delta)
		q.lastDropTime = now
		return true
	}

	return false
}

// Close closes the queue
func (q *codelQueue) Close() {
	q.mu.Lock()
	defer q.mu.Unlock()
	q.closed = true
	close(q.newPacket)
}

// LinkSettings defines the network characteristics for a simulated link direction
type LinkSettings struct {
	// BitsPerSecond specifies the bandwidth limit in bits per second
	BitsPerSecond int

	// Latency specifies a fixed network delay for all packets
	// If both Latency and LatencyFunc are set, LatencyFunc takes precedence
	Latency time.Duration

	// LatencyFunc computes the network delay for each packet
	// This allows variable latency based on packet source/destination
	// If nil, Latency field is used instead
	LatencyFunc func(Packet) time.Duration

	// MTU (Maximum Transmission Unit) specifies the maximum packet size in bytes
	MTU int
}

// SimulatedLink simulates a bidirectional network link with variable latency,
// bandwidth limiting, and CoDel-based bufferbloat mitigation
type SimulatedLink struct {
	// Internal state for lifecycle management
	closed chan struct{}
	wg     sync.WaitGroup

	// CoDel queues for bufferbloat control
	downstreamQueue *codelQueue
	upstreamQueue   *codelQueue

	// Rate limiters enforce bandwidth constraints
	upLimiter   *rate.Limiter
	downLimiter *rate.Limiter

	// Configuration for link characteristics
	UplinkSettings   LinkSettings
	DownlinkSettings LinkSettings

	// Packet routing interfaces
	UploadPacket   Router
	downloadPacket PacketReceiver
}

func (l *SimulatedLink) AddNode(addr net.Addr, receiver PacketReceiver) {
	l.downloadPacket = receiver
}

func (l *SimulatedLink) Start() {
	if l.downloadPacket == nil {
		panic("SimulatedLink.Start() called without having added a packet receiver")
	}

	l.closed = make(chan struct{})

	// Sane defaults
	if l.DownlinkSettings.MTU == 0 {
		l.DownlinkSettings.MTU = 1400
	}
	if l.UplinkSettings.MTU == 0 {
		l.UplinkSettings.MTU = 1400
	}

	// Initialize CoDel queues with 5ms target and 100ms interval
	const target = 5 * time.Millisecond
	const interval = 100 * time.Millisecond
	l.downstreamQueue = newCodelQueue(target, interval)
	l.upstreamQueue = newCodelQueue(target, interval)

	// Initialize rate limiters
	const burstSizeInPackets = 16
	l.upLimiter = newRateLimiter(l.UplinkSettings.BitsPerSecond, l.UplinkSettings.MTU*burstSizeInPackets)
	l.downLimiter = newRateLimiter(l.DownlinkSettings.BitsPerSecond, l.DownlinkSettings.MTU*burstSizeInPackets)

	l.wg.Add(2)
	go l.backgroundDownlink()
	go l.backgroundUplink()
}

func (l *SimulatedLink) Close() error {
	close(l.closed)
	l.downstreamQueue.Close()
	l.upstreamQueue.Close()
	l.wg.Wait()
	return nil
}

func (l *SimulatedLink) backgroundDownlink() {
	defer l.wg.Done()

	for {
		select {
		case <-l.closed:
			return
		default:
		}

		// Dequeue a packet (this will block until packet is ready for delivery)
		p, ok := l.downstreamQueue.Dequeue()
		if !ok {
			return
		}

		// Calculate sojourn time (time spent in queue)
		sojournTime := time.Since(p.DeliveryTime)

		// Check if CoDel wants to drop this packet
		shouldDrop := l.downstreamQueue.shouldDrop(sojournTime)
		if shouldDrop {
			// Drop the packet and continue to next one
			continue
		}

		// Apply rate limiting before delivery
		l.downLimiter.WaitN(context.Background(), len(p.buf))

		// Deliver the packet
		l.downloadPacket.RecvPacket(p.Packet)
	}
}

func (l *SimulatedLink) backgroundUplink() {
	defer l.wg.Done()

	for {
		select {
		case <-l.closed:
			return
		default:
		}

		// Dequeue a packet (this will block until packet is ready for delivery)
		p, ok := l.upstreamQueue.Dequeue()
		if !ok {
			return
		}

		// Calculate sojourn time (time spent in queue)
		sojournTime := time.Since(p.DeliveryTime)

		// Check if CoDel wants to drop this packet
		shouldDrop := l.upstreamQueue.shouldDrop(sojournTime)
		if shouldDrop {
			// Drop the packet and continue to next one
			continue
		}

		// Apply rate limiting before delivery
		l.upLimiter.WaitN(context.Background(), len(p.buf))

		// Deliver the packet
		_ = l.UploadPacket.SendPacket(p.Packet)
	}
}

func (l *SimulatedLink) SendPacket(p Packet) error {
	if len(p.buf) > l.UplinkSettings.MTU {
		// Drop packet if it's too large
		return nil
	}

	// Calculate delivery time based on latency
	var latency time.Duration
	if l.UplinkSettings.LatencyFunc != nil {
		latency = l.UplinkSettings.LatencyFunc(p)
	} else {
		latency = l.UplinkSettings.Latency
	}
	deliveryTime := time.Now().Add(latency)

	// Enqueue packet with delivery time to CoDel queue
	// Rate limiting happens after dequeue in background goroutine
	l.upstreamQueue.Enqueue(&packetWithDeliveryTime{
		Packet:       p,
		DeliveryTime: deliveryTime,
	})

	return nil
}

func (l *SimulatedLink) RecvPacket(p Packet) {
	if len(p.buf) > l.DownlinkSettings.MTU {
		// Drop packet if it's too large
		return
	}

	// Calculate delivery time based on latency
	var latency time.Duration
	if l.DownlinkSettings.LatencyFunc != nil {
		latency = l.DownlinkSettings.LatencyFunc(p)
	} else {
		latency = l.DownlinkSettings.Latency
	}
	deliveryTime := time.Now().Add(latency)

	// Enqueue packet with delivery time to CoDel queue
	// Rate limiting happens after dequeue in background goroutine
	l.downstreamQueue.Enqueue(&packetWithDeliveryTime{
		Packet:       p,
		DeliveryTime: deliveryTime,
	})
}
