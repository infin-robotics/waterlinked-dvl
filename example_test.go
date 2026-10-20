package dvl_test

import (
	"context"
	"errors"
	"fmt"
	"log"
	"net"
	"net/netip"
	"time"

	dvl "github.com/infin-robotics/waterlinked-dvl"
)

func ExampleDiscover() {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	device, err := dvl.Discover(ctx, netip.MustParsePrefix("192.168.194.0/24"))
	if errors.Is(err, dvl.ErrNoDevices) {
		log.Fatal("no DVL on this subnet")
	} else if err != nil {
		log.Fatal(err)
	}

	// Discovery only reads the HTTP identity; the data connection is yours.
	address := netip.AddrPortFrom(device.Addr, dvl.DefaultPort).String()
	conn, err := dvl.Dial(ctx, address)
	if err != nil {
		log.Fatal(err)
	}
	defer conn.Close()
	log.Printf("connected to %s at %s", device.ProductName, address)
}

func ExampleDiscoverAll() {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	devices, err := dvl.DiscoverAll(ctx, netip.MustParsePrefix("192.168.194.0/24"))
	if err != nil {
		log.Printf("scan interrupted: %v", err) // devices holds what was found
	}
	for _, device := range devices {
		log.Printf("%s at %s", device.ProductName, device.Addr)
	}
}

func ExampleConn_VelocityReports() {
	ctx := context.Background()
	address := fmt.Sprintf("192.168.194.95:%d", dvl.DefaultPort)

	conn, err := dvl.Dial(ctx, address)
	if err != nil {
		log.Fatal(err)
	}
	defer conn.Close()

	for sample := range conn.VelocityReports() {
		if sample.DroppedBefore != 0 {
			log.Printf("DVL velocity consumer fell behind: dropped %d reports", sample.DroppedBefore)
		}

		report := sample.Report
		if report.Measurement == nil {
			continue
		}
		log.Printf(
			"%s-relative velocity: x=%.3f y=%.3f z=%.3f m/s",
			report.Reference,
			report.Measurement.Velocity.X,
			report.Measurement.Velocity.Y,
			report.Measurement.Velocity.Z,
		)
	}

	if err := conn.Err(); err != nil && !errors.Is(err, net.ErrClosed) {
		log.Printf("DVL connection ended: %v", err)
	}
}

func ExampleConn_UnhandledFrames() {
	ctx := context.Background()
	address := fmt.Sprintf("192.168.194.95:%d", dvl.DefaultPort)

	conn, err := dvl.Dial(ctx, address)
	if err != nil {
		log.Fatal(err)
	}
	defer conn.Close()

	for sample := range conn.UnhandledFrames() {
		frame := sample.Report
		if frame.Err != nil {
			// A malformed report, an unknown protocol major, or a response with no
			// command currently waiting for it (unsolicited, or late for one that
			// already finished): Err explains why it could not be decoded. A
			// response tied to a still-waiting command that cannot be decoded fails
			// that command's own call instead of arriving here.
			log.Printf("undecodable %q frame: %v", frame.Type, frame.Err)
			continue
		}
		// A well-formed report of a type this version of the package does not
		// model yet.
		log.Printf("unknown report type %q: %s", frame.Type, frame.Raw)
	}
}

func ExampleDialer() {
	ctx := context.Background()
	address := fmt.Sprintf("192.168.194.95:%d", dvl.DefaultPort)
	dialer := &dvl.Dialer{
		IdleTimeout: 5 * time.Second,
	}

	// runEpoch consumes one connection until it ends, reporting why.
	runEpoch := func() error {
		conn, err := dialer.Dial(ctx, address)
		if err != nil {
			return err
		}
		defer conn.Close()

		velocityReports := conn.VelocityReports()
		for {
			select {
			case sample, ok := <-velocityReports:
				if !ok {
					velocityReports = nil
					continue
				}
				if sample.Report.Measurement != nil {
					log.Printf("velocity: %+v", sample.Report.Measurement.Velocity)
				}
			case <-conn.Done():
				return conn.Err()
			}
		}
	}

	// A supervisor loop: redial whenever the connection ends, including when
	// the idle timeout fires because the device has gone quiet.
	for {
		if err := runEpoch(); err != nil && !errors.Is(err, net.ErrClosed) {
			log.Printf("DVL connection ended, redialing: %v", err)
		}
		time.Sleep(time.Second)
	}
}

func ExampleConn_UpdateConfig() {
	ctx := context.Background()
	address := fmt.Sprintf("192.168.194.95:%d", dvl.DefaultPort)

	conn, err := dvl.Dial(ctx, address)
	if err != nil {
		log.Fatal(err)
	}
	defer conn.Close()

	before, err := conn.Config(ctx)
	if err != nil {
		log.Fatal(err)
	}

	desiredSpeedOfSound := 1480.0
	if before.SpeedOfSound != desiredSpeedOfSound {
		err = conn.UpdateConfig(ctx, dvl.ConfigUpdate{
			SpeedOfSound: &desiredSpeedOfSound,
		})
		if err != nil {
			log.Fatal(err)
		}
	}

	after, err := conn.Config(ctx)
	if err != nil {
		log.Fatal(err)
	}
	if after.SpeedOfSound != desiredSpeedOfSound {
		log.Fatalf(
			"DVL accepted speed-of-sound update but reports %.1f m/s",
			after.SpeedOfSound,
		)
	}
}

func ExampleConn() {
	ctx := context.Background()
	address := fmt.Sprintf("192.168.194.95:%d", dvl.DefaultPort)

	conn, err := dvl.Dial(ctx, address)
	if err != nil {
		log.Fatal(err)
	}
	defer conn.Close()

	velocityReports := conn.VelocityReports()
	deadReckoningReports := conn.DeadReckoningReports()
	unhandledFrames := conn.UnhandledFrames()
	for {
		select {
		case sample, ok := <-velocityReports:
			if !ok {
				velocityReports = nil
				continue
			}
			log.Printf("Velocity: %v", sample.Report)
		case sample, ok := <-deadReckoningReports:
			if !ok {
				deadReckoningReports = nil
				continue
			}
			log.Printf("Dead reckoning: %v", sample.Report)
		case sample, ok := <-unhandledFrames:
			if !ok {
				unhandledFrames = nil
				continue
			}
			// An unhandled frame is data the client could not model, not a
			// connection fault: Err is nil for a report type this version of the
			// package does not know.
			log.Printf("Unhandled %q frame: %s (%v)", sample.Report.Type, sample.Report.Raw, sample.Report.Err)
		case <-conn.Done():
			if err := conn.Err(); err != nil && !errors.Is(err, net.ErrClosed) {
				log.Printf("DVL connection ended: %v", err)
			}
			return
		}
	}
}

func ExampleConn_Info() {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	conn, err := dvl.Dial(ctx, "192.168.194.95:16171")
	if err != nil {
		log.Fatal(err)
	}
	defer conn.Close()

	info, err := conn.Info(ctx)
	if err != nil {
		log.Fatal(err)
	}
	fmt.Printf("%s (%s) firmware %s ready=%t\n",
		info.ProductName, info.Variant, info.FirmwareVersionShort, info.Ready)
}

func ExampleCommandError() {
	ctx := context.Background()

	conn, err := dvl.Dial(ctx, "192.168.194.95:16171")
	if err != nil {
		log.Fatal(err)
	}
	defer conn.Close()

	mode := dvl.RangeMode("2<=3")
	err = conn.UpdateConfig(ctx, dvl.ConfigUpdate{RangeMode: &mode})

	var rejected *dvl.CommandError
	var protocol *dvl.ProtocolError
	switch {
	case err == nil:
		log.Print("range mode updated")
	case errors.As(err, &rejected):
		// The device understood the command and refused it, so the connection
		// is still usable and the device's message says why.
		log.Printf("device rejected %s: %s", rejected.Command, rejected.Message)
	case errors.As(err, &protocol):
		// The device answered with something this package could not decode.
		// Only a timeout or a mismatched response ends the connection; check
		// conn.Err to tell the two apart.
		log.Printf("protocol error: %v (connection error: %v)", protocol, conn.Err())
	default:
		log.Printf("update failed: %v", err)
	}
}
