package simnet_test

import (
	"fmt"
	"net"
	"testing"
	"testing/synctest"
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

// Test ping between two nodes with a 3 second delay between pings.
func TestSimnet_pingWithDelay(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {

		// Create the simulated network and two endpoints
		n := &simnet.Simnet{}
		latency := 400 * time.Millisecond
		settings := simnet.NodeBiDiLinkSettings{
			Downlink: simnet.LinkSettings{BitsPerSecond: 10 * simnet.Mibps, Latency: latency / 2},
			Uplink:   simnet.LinkSettings{BitsPerSecond: 10 * simnet.Mibps, Latency: latency / 2},
		}

		addrA := &net.UDPAddr{IP: net.ParseIP("1.0.0.1"), Port: 9001}
		addrB := &net.UDPAddr{IP: net.ParseIP("1.0.0.2"), Port: 9002}

		client := n.NewEndpoint(addrA, settings)
		server := n.NewEndpoint(addrB, settings)

		err := n.Start()
		if err != nil {
			t.Fatalf("Failed to start simnet: %v", err)
		}
		defer n.Close()

		// Simple echo server using the returned PacketConn
		serverDone := make(chan error, 1)
		go func() {
			buf := make([]byte, 1024)

			// Handle first ping
			server.SetReadDeadline(time.Now().Add(10 * time.Second))
			n, src, err := server.ReadFrom(buf)
			if err != nil {
				serverDone <- fmt.Errorf("server failed to read first ping: %w", err)
				return
			}
			if string(buf[:n]) != "ping1" {
				serverDone <- fmt.Errorf("server expected 'ping1', got '%s'", string(buf[:n]))
				return
			}
			_, err = server.WriteTo(append([]byte("pong: "), buf[:n]...), src)
			if err != nil {
				serverDone <- fmt.Errorf("server failed to write first pong: %w", err)
				return
			}

			// Handle second ping
			server.SetReadDeadline(time.Now().Add(10 * time.Second))
			n, src, err = server.ReadFrom(buf)
			if err != nil {
				serverDone <- fmt.Errorf("server failed to read second ping: %w", err)
				return
			}
			if string(buf[:n]) != "ping2" {
				serverDone <- fmt.Errorf("server expected 'ping2', got '%s'", string(buf[:n]))
				return
			}
			_, err = server.WriteTo(append([]byte("pong: "), buf[:n]...), src)
			if err != nil {
				serverDone <- fmt.Errorf("server failed to write second pong: %w", err)
				return
			}
			serverDone <- nil
		}()

		// Client sends first ping
		client.SetReadDeadline(time.Now().Add(1 * time.Second))
		_, err = client.WriteTo([]byte("ping1"), addrB)
		if err != nil {
			t.Fatalf("Client failed to write ping1: %v", err)
		}

		buf := make([]byte, 1024)
		nRead, _, err := client.ReadFrom(buf)
		if err != nil {
			t.Fatalf("Client failed to read pong1: %v", err)
		}
		if string(buf[:nRead]) != "pong: ping1" {
			t.Fatalf("Client expected 'pong: ping1', got '%s'", string(buf[:nRead]))
		}

		// Wait 3 seconds before sending second ping
		time.Sleep(3 * time.Second)

		// Client sends second ping
		client.SetReadDeadline(time.Now().Add(1 * time.Second))
		_, err = client.WriteTo([]byte("ping2"), addrB)
		if err != nil {
			t.Fatalf("Client failed to write ping2: %v", err)
		}

		nRead, _, err = client.ReadFrom(buf)
		if err != nil {
			t.Fatalf("Client failed to read pong2: %v", err)
		}
		if string(buf[:nRead]) != "pong: ping2" {
			t.Fatalf("Client expected 'pong: ping2', got '%s'", string(buf[:nRead]))
		}

		// Wait for server to complete
		if err := <-serverDone; err != nil {
			t.Fatalf("Server error: %v", err)
		}
	})
}
