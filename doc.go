// Package dvl is a client for the Water Linked DVL A50 and A125 TCP JSON API.
//
// A Doppler velocity log (DVL) measures the velocity of the vehicle carrying
// it by timing acoustic pings reflected off the sea bottom or, in water
// tracking mode, off the water column. The A50 and A125 publish those
// measurements as newline-delimited JSON on TCP port 16171. On the same
// connection they accept commands that read identity and configuration,
// change configuration, reset dead reckoning, and calibrate the gyroscope.
// This package decodes the reports into typed Go values and delivers them on
// channels. Each command becomes a method that sends it and waits for its
// response.
//
// The package deliberately stops there. It does not reconnect, filter, fuse,
// or rotate measurements into the vehicle's frame. Those decisions depend on
// the installation and are easy to get silently wrong inside a driver, so
// they are left to the caller.
//
// # Connecting
//
// [Dial] opens a connection with default settings. A [Dialer] configures the
// TCP dialer, an idle timeout, the command timeout, and the per-stream report
// buffer:
//
//	dialer := dvl.Dialer{IdleTimeout: 5 * time.Second}
//	conn, err := dialer.Dial(ctx, "192.168.194.95:16171")
//
// A [Conn] is one TCP connection epoch. It ends when the caller calls
// [Conn.Close], when the transport fails, or when the protocol reaches a
// state the connection cannot recover from; the [Conn] documentation lists
// the exact conditions. A Conn never reconnects. [Conn.Done] closes when the
// connection ends and [Conn.Err] reports why, so a supervisor loop dials a
// new connection and decides how long to wait before doing so. The Dialer
// example shows that loop.
//
// The context passed to Dial bounds only the dial. Cancelling it afterwards
// has no effect on the connection.
//
// # Discovering a device
//
// [Discover] and [DiscoverAll] find DVLs on an IPv4 subnet you name, using
// the device's HTTP identity endpoint rather than its TCP port. They return a
// [Device] whose Addr, combined with [DefaultPort], is the address to pass to
// [Dial]. Neither opens a data connection. Their documentation gives the
// exact probe, scope limits, and cancellation behaviour.
//
// # Reports
//
// The device sends reports on its own schedule, whether or not anything is
// listening. The connection reads every frame and routes it onto one of
// three independent streams:
//
//   - [Conn.VelocityReports] carries velocity and velocity_water reports as
//     [VelocityReport] values.
//   - [Conn.DeadReckoningReports] carries position_local reports as
//     [DeadReckoningReport] values.
//   - [Conn.UnhandledFrames] carries everything else as [UnhandledFrame]
//     values: reports of a type this package does not model, frames it could
//     not decode, and responses no command was waiting for.
//
// Each stream is a bounded queue that drops its oldest item when full, so a
// slow or absent consumer on one stream never delays another stream or a
// command response. Every delivered [Sample] carries a DroppedBefore count of
// the items discarded from that stream since the previous delivery. A
// consumer that needs every report should size [Dialer.ReportBuffer] for its
// worst-case pause and treat a non-zero count as a fault.
//
// Each stream method returns the same channel on every call. Two goroutines
// receiving from it divide the reports between them rather than each seeing
// all of them. The channels close when the connection ends, and anything
// still buffered at that point is discarded.
//
// # Velocity and lock
//
// A DVL has lock when enough beams reflect off a surface for it to compute a
// velocity. Without lock the device still sends a velocity report at its
// usual rate, and that report still carries velocity, figure of merit,
// covariance, and altitude fields holding whatever the device last computed.
// Consuming those numbers as if they were current is a common DVL
// integration bug.
//
// [VelocityReport.Measurement] is therefore nil whenever the device reports
// velocity_valid false. The rest of the report, in particular
// [VelocityReport.Transducers] and [VelocityReport.Status], is populated on
// every report so a caller can diagnose why lock was lost.
//
// [VelocityReport.Reference] says what the velocity is relative to. Bottom
// tracking is the normal mode. When [Config.RangeMode] is
// [RangeModeWaterTracking] the device reports velocity relative to the water
// column instead, and [VelocityMeasurement.Altitude] is nil because there is
// no surface to measure a distance to.
//
// # Frames, units, and time
//
// Every vector is in the frame the device emits, which is right-handed with
// X forward, Y to starboard, and Z down towards the transducers. When
// [Config.MountingYawOffset] is non-zero the device rotates its output about
// Z so that X aligns with the vehicle's forward axis. It never compensates
// for mounting pitch or roll. The dead-reckoning frame is the emitted frame
// as it was when the device started or [Conn.ResetDeadReckoning] was last
// accepted. This package does not transform measurements further.
//
// Units follow the device: velocity in metres per second, distance and
// altitude in metres, covariance in (m/s)², angles in degrees, and signal
// levels in dBm. Durations are [time.Duration] values and timestamps are
// [time.Time] values in UTC.
//
// Timestamps come from the device's own clock, which is only as accurate as
// the device's NTP configuration. [VelocityReport.ValidAt] is the instant of
// the surface reflection and [VelocityReport.TransmittedAt] the instant the
// report was queued for sending. The difference is the acoustic round trip
// plus on-device processing and can exceed 100 milliseconds.
// [VelocityReport.Interval] is the device's own measure of time since its
// previous velocity report and does not depend on clock synchronisation.
//
// # Commands
//
// [Conn.Info], [Conn.Config], [Conn.UpdateConfig], [Conn.ResetDeadReckoning],
// and [Conn.CalibrateGyro] each send one command and block until its
// response arrives, the context is done, or the connection ends. Responses
// carry the command name but no request identifier, so the connection runs
// commands one at a time. Calling them from several goroutines is safe but
// serialised. Reports keep flowing while a command is waiting.
//
// A successful response means the device accepted the command, not that its
// effect is visible yet. [Conn.UpdateConfig] documents the read-back pattern
// for callers that need to confirm a change.
//
// # Errors
//
// Errors fall into a few families that callers can test with [errors.Is]
// and [errors.As]:
//
//   - A *[CommandError] means the device rejected a well-formed command. Its
//     Message field carries the device's own explanation.
//   - A *[ProtocolError] means the device sent something this package could
//     not decode or correlate. Its Err field retains the underlying cause.
//   - [net.ErrClosed] is the terminal error after [Conn.Close] and the error
//     returned by operations that Close interrupted.
//   - [io.EOF] is the terminal error when the device closed the connection.
//   - The terminal error wraps [os.ErrDeadlineExceeded] when the idle
//     timeout fires and [context.DeadlineExceeded] when the command timeout
//     fires.
//   - A Dial or command cut short by the caller's own context returns that
//     context's error unwrapped.
//
// A dropped report is not an error; see Reports above.
//
// # Compatibility
//
// The package targets A50 and A125 devices on firmware 2.4.0 or later, which
// speak protocol json_v3.1 or later. It decodes any json_v3 minor version and
// preserves unknown status bits and unknown report types rather than rejecting
// them. [Conn.Info] needs firmware 2.7.2, which introduced the underlying
// get_version_info command; its behaviour on earlier firmware has not been
// verified. Live validation covers an A50 running firmware 2.7.2. The A125
// uses the same API but has not been exercised with this package.
//
// # Command-line tool
//
// The waterlinked-dvl command in this module exercises the API against a
// real device and is the quickest way to confirm that a DVL is reachable and
// reporting. See [github.com/infin-robotics/waterlinked-dvl/cmd/waterlinked-dvl].
//
// Water Linked documents the protocol itself in the [TCP JSON API] page and
// the frame conventions in the [Axes] page.
//
// [TCP JSON API]: https://docs.waterlinked.com/dvl/dvl-json-protocol/
// [Axes]: https://docs.waterlinked.com/dvl/axes/
package dvl
