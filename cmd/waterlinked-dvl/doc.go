// Command waterlinked-dvl inspects and operates a Water Linked DVL A50 or
// A125 from the shell. It is a thin wrapper over the exported dvl API and
// exists to confirm that a device is reachable and reporting before any
// integration code is written.
//
// Install it with:
//
//	go install github.com/infin-robotics/waterlinked-dvl/cmd/waterlinked-dvl@latest
//
// Every command takes -address, a host or host:port, which defaults to the
// A50's factory address 192.168.194.95 on port 16171, and -json, which
// switches the output from human-readable text to JSON.
//
// # Commands
//
// info prints the device's identity, firmware version, and readiness. It is
// the first thing to run against a new installation:
//
//	waterlinked-dvl info -address 192.168.194.95
//
// watch prints delivered velocity and dead-reckoning reports plus unhandled
// frames, one per line, until interrupted or until -duration elapses. Each line
// includes the number of older items dropped from that stream. With -json each
// line is one JSON object, so a capture can be replayed later:
//
//	waterlinked-dvl watch -duration 30s -json > reports.jsonl
//
// config get prints the complete device configuration. config set applies
// only the fields given as flags, reads the configuration back, and reports
// failure if the device did not observe the change:
//
//	waterlinked-dvl config set -speed-of-sound 1480
//	waterlinked-dvl config set -periodic-cycling-enabled=false
//
// reset-dead-reckoning zeroes the device's local position and attitude.
// calibrate-gyro calibrates the gyroscope and needs the device held
// stationary for the whole operation, which can take 15 seconds.
//
// Run waterlinked-dvl help, or any command with -h, for the full flag list.
//
// # Exit status
//
// The command exits 0 when the operation completed, and 1 with a message on
// standard error otherwise. Interrupting watch with Ctrl-C exits 0.
package main
