package dvl

import (
	"net"
	"testing"
	"time"
)

func TestBufferedSamplesRetainFrameReceiptTime(t *testing.T) {
	release := make(chan struct{})
	defer close(release)
	peer := startTestPeer(t, func(socket net.Conn) error {
		for _, frame := range []any{velocityFrame(1), deadReckoningFrame(), map[string]any{"type": "future_report", "format": "json_v3.3"}} {
			if err := writeTestFrame(socket, frame); err != nil {
				return err
			}
		}
		if _, err := readTestCommand(socket); err != nil {
			return err
		}
		if err := writeTestFrame(socket, infoResponse(true, "")); err != nil {
			return err
		}
		<-release
		return nil
	})
	before := time.Now()
	conn := dialTestPeer(t, peer.address)
	// The shared reader must enqueue every report before completing Info.
	if _, err := conn.Info(t.Context()); err != nil {
		t.Fatal(err)
	}
	queuedBy := time.Now()
	assertReceipt := func(name string, received time.Time) {
		t.Helper()
		if received.Before(before) || received.After(queuedBy) {
			t.Errorf("%s receipt=%v, want between dial and command completion [%v, %v]", name, received, before, queuedBy)
		}
		// ReceivedAt must retain the monotonic component, not just wall time.
		if received == received.Round(0) {
			t.Errorf("%s receipt has no monotonic reading: %v", name, received)
		}
	}
	select {
	case sample := <-conn.VelocityReports():
		assertReceipt("velocity", sample.ReceivedAt)
	case <-time.After(time.Second):
		t.Fatal("velocity timeout")
	}
	select {
	case sample := <-conn.DeadReckoningReports():
		assertReceipt("position", sample.ReceivedAt)
	case <-time.After(time.Second):
		t.Fatal("position timeout")
	}
	select {
	case sample := <-conn.UnhandledFrames():
		assertReceipt("unhandled", sample.ReceivedAt)
	case <-time.After(time.Second):
		t.Fatal("unhandled timeout")
	}
}
