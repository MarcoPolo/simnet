//go:build go1.25

package simnet

import (
	"fmt"
	"math"
	"net"
	"net/netip"
	"sync"
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"

	"github.com/marcopolo/simnet/internal/require"
)

type testRouter struct {
	onRecv func(p Packet)
}

func (r *testRouter) RecvPacket(p Packet) {
	r.onRecv(p)
}

func (r *testRouter) AddNode(addr net.Addr, receiver PacketReceiver) {
	r.onRecv = receiver.RecvPacket
}

func TestLinkDriver(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		const mtu = 1500
		var recvdPackets atomic.Uint32
		tr := testRouter{
			onRecv: func(p Packet) {
				recvdPackets.Add(1)
			},
		}

		closeSignal := make(chan struct{})
		ld := newLinkDriver(
			5*time.Millisecond,
			100*time.Millisecond,
			10*mtu,
			128,
			mtu,
			50*Mibps,
			&tr,
			closeSignal,
		)

		var wg sync.WaitGroup
		ld.Start(&wg)

		defer wg.Wait()
		defer close(closeSignal)

		ld.RecvPacket(Packet{
			buf:  []byte("Hello World"),
			To:   net.UDPAddrFromAddrPort(netip.MustParseAddrPort("1.2.3.4:1234")),
			From: net.UDPAddrFromAddrPort(netip.MustParseAddrPort("1.2.3.5:1234")),
		})

		require.Equal(t, uint32(0), recvdPackets.Load())
		time.Sleep(10 * time.Millisecond)
		require.Equal(t, uint32(1), recvdPackets.Load())
	})
}

func TestBandwidthLimiter_synctest(t *testing.T) {
	for _, testUpload := range []bool{true} {
		t.Run(fmt.Sprintf("testing upload=%t", testUpload), func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				const expectedSpeed = 10 * Mibps
				const MTU = 1400
				linkSettings := LinkSettings{
					BitsPerSecond: expectedSpeed,
					MTU:           MTU,
				}
				bidiLinkSettings := NodeBiDiLinkSettings{
					Uplink:   linkSettings,
					Downlink: linkSettings,
				}

				recvStartTimeChan := make(chan time.Time, 1)
				recvStarted := false
				bytesRead := 0
				packetHandler := func(p Packet) {
					if !recvStarted {
						recvStarted = true
						recvStartTimeChan <- time.Now()
					}
					bytesRead += len(p.buf)
				}

				router := &testRouter{}
				router.onRecv = packetHandler
				closeSignal := make(chan struct{})
				var wg sync.WaitGroup
				link := NewSimlink(
					closeSignal,
					bidiLinkSettings,
					router,
					router,
				)

				link.Start(&wg)

				// Send 10MiB of data
				chunk := make([]byte, MTU)
				bytesSent := 0

				{
					totalBytes := 10 << 20
					// Blast a bunch of packets
					for bytesSent < totalBytes {
						// This sleep shouldn't limit the speed. 1400 Bytes/100us = 14KB/ms = 14MB/s = 14*8 Mbps
						// but it acts as a simple pacer to avoid just dropping the packets when the link is saturated.
						time.Sleep(100 * time.Microsecond)
						p := Packet{
							buf:  chunk,
							To:   net.UDPAddrFromAddrPort(netip.MustParseAddrPort("1.2.3.4:1234")),
							From: net.UDPAddrFromAddrPort(netip.MustParseAddrPort("1.2.3.5:1234")),
						}
						if testUpload {
							link.up.RecvPacket(p)
						} else {
							link.down.RecvPacket(p)
						}
						bytesSent += len(chunk)
					}
				}

				// Wait for delayed packets to be sent
				time.Sleep(40 * time.Millisecond)
				t.Logf("sent: %d", bytesSent)

				close(closeSignal)
				wg.Wait()

				t.Logf("read: %d", bytesRead)
				recvStartTime := <-recvStartTimeChan
				duration := time.Since(recvStartTime)

				observedSpeed := 8 * float64(bytesRead) / duration.Seconds()
				t.Logf("observed speed: %f Mbps over %s", observedSpeed/Mibps, duration)
				percentErrorSpeed := math.Abs(observedSpeed-float64(expectedSpeed)) / float64(expectedSpeed)
				t.Logf("observed speed: %f Mbps, expected speed: %d Mbps, percent error: %f", observedSpeed/Mibps, expectedSpeed/Mibps, percentErrorSpeed)
				if percentErrorSpeed > 0.20 {
					t.Fatalf("observed speed %f Mbps is too far from expected speed %d Mbps. Percent error: %f", observedSpeed/Mibps, expectedSpeed/Mibps, percentErrorSpeed)
				}
			})
		})
	}
}
