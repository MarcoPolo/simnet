package simnet_test

import (
	"fmt"
	"net"
	"time"

	"github.com/marcopolo/simnet"
)

// Example showing a simple echo using Simnet and the returned net.PacketConn.
func ExampleSimnet_echo() {
	// Create the simulated network and two endpoints
	n := &simnet.Simnet{}
	settings := simnet.NodeBiDiLinkSettings{
		Downlink: simnet.LinkSettings{BitsPerSecond: 10 * simnet.Mibps, Latency: 5 * time.Millisecond},
		Uplink:   simnet.LinkSettings{BitsPerSecond: 10 * simnet.Mibps, Latency: 5 * time.Millisecond},
	}

	addrA := &net.UDPAddr{IP: net.ParseIP("1.0.0.1"), Port: 9001}
	addrB := &net.UDPAddr{IP: net.ParseIP("1.0.0.2"), Port: 9002}

	client := n.NewEndpoint(addrA, settings)
	server := n.NewEndpoint(addrB, settings)

	_ = n.Start()
	defer n.Close()

	// Simple echo server using the returned PacketConn
	done := make(chan struct{})
	go func() {
		defer close(done)
		buf := make([]byte, 1024)
		server.SetReadDeadline(time.Now().Add(2 * time.Second))
		n, src, err := server.ReadFrom(buf)
		if err != nil {
			return
		}
		server.WriteTo(append([]byte("echo: "), buf[:n]...), src)
	}()

	// Client sends a message and waits for the echo response
	client.SetReadDeadline(time.Now().Add(2 * time.Second))
	_, _ = client.WriteTo([]byte("ping"), addrB)

	buf := make([]byte, 1024)
	nRead, _, _ := client.ReadFrom(buf)
	fmt.Println(string(buf[:nRead]))

	<-done

	// Output:
	// echo: ping
}
