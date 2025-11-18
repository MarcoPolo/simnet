package simnet

import (
	"math"
	"testing"
	"testing/synctest"
	"time"
)

type countingReceiver struct {
	totalBytes int
}

func (c *countingReceiver) RecvPacket(p Packet) {
	c.totalBytes += len(p.buf)
}

func TestRateLinkObservedBandwidth(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		const (
			mtu        = 1500
			bandwidth  = 50 * Mibps
			burstSize  = 10 * mtu
			packets    = 20_000
			totalBytes = packets * mtu
		)

		receiver := &countingReceiver{}
		link := NewRateLink(bandwidth, burstSize, receiver)

		chunk := make([]byte, mtu)

		start := time.Now()
		for range packets {
			p := Packet{buf: chunk}
			time.Sleep(link.Reserve(time.Now(), len(p.buf)))
			link.RecvPacket(p)
		}
		duration := time.Since(start)

		if receiver.totalBytes != totalBytes {
			t.Fatalf("expected receiver to get %d bytes, got %d", totalBytes, receiver.totalBytes)
		}

		observedBandwidth := 8 * float64(totalBytes) / duration.Seconds()
		diff := math.Abs(observedBandwidth - float64(bandwidth))
		allowedError := 0.10 * float64(bandwidth)
		if diff > allowedError {
			t.Fatalf("observed bandwidth %f bps differs from expected %d bps by %f bps (allowed %f)", observedBandwidth, bandwidth, diff, allowedError)
		}
	})
}
