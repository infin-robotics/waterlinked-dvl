# waterlinked-dvl

[![CI][ci-badge]][ci]
[![Go Reference][reference-badge]][go-reference]
[![License: Apache-2.0][license-badge]][license]

A Go client for the Water Linked DVL A50 and A125 TCP JSON API. It turns the
device's velocity, dead-reckoning, identity, and configuration messages into
typed Go values and delivers reports on channels, while keeping the timing,
lock state, covariance, and per-beam detail the device provides.

The package connects, decodes, and delivers. It doesn't reconnect, filter, or
rotate measurements into your vehicle's frame. Those depend on your
installation, so they're left to you.

## Supported hardware

The client targets DVL A50 and A125 running firmware 2.4.0 or later, which
speak TCP JSON protocol json_v3.1 or later. Reading device identity with
`Info` needs firmware 2.7.2, where the device gained the underlying command.
Live validation covers an A50 running firmware 2.7.2. The A125 uses the same
API but has not been exercised with this client.

## Installation

```console
go get github.com/infin-robotics/waterlinked-dvl
```

The module requires Go 1.25 or later and has no dependencies outside the
standard library.

## Quick start

Check that the DVL is reachable before writing any code. The diagnostic CLI
uses the same exported API as the package:

```console
go install github.com/infin-robotics/waterlinked-dvl/cmd/waterlinked-dvl@latest
waterlinked-dvl info -address 192.168.194.95
waterlinked-dvl watch -address 192.168.194.95
```

`info` prints the device identity and firmware. `watch` prints delivered
velocity and dead-reckoning reports plus unhandled frames, with a dropped-item
count for each stream, so you can see the DVL acquire and lose lock. Once that
works, the same thing in Go:

```go
package main

import (
	"context"
	"log"

	dvl "github.com/infin-robotics/waterlinked-dvl"
)

func main() {
	conn, err := dvl.Dial(context.Background(), "192.168.194.95:16171")
	if err != nil {
		log.Fatal(err)
	}
	defer conn.Close()

	for sample := range conn.VelocityReports() {
		report := sample.Report
		if report.Measurement == nil {
			continue // the DVL has no lock; the device's numbers are stale
		}
		v := report.Measurement.Velocity
		log.Printf("%s velocity x=%.3f y=%.3f z=%.3f m/s", report.Reference, v.X, v.Y, v.Z)
	}
	log.Printf("connection ended: %v", conn.Err())
}
```

The velocity channel closes when the connection ends, and `conn.Err` says why.
A production consumer wraps this in a redial loop; the `Dialer` example in the
package reference shows one.

## What you need to know

Four things about the device and this package are the usual surprises. The
[package documentation][go-reference] covers each in more depth.

- **No lock means no measurement.** After losing lock the DVL keeps sending
  velocity reports with stale numbers in them. The package sets
  `Measurement` to nil on those reports instead of passing the numbers
  through, so check for nil before reading velocity, covariance, or altitude.
- **Reports and commands share one connection.** Velocity, dead-reckoning,
  and unhandled frames arrive on three independent, bounded streams. A slow
  consumer on one stream loses that stream's oldest reports and never blocks
  the others or a command response. Each `Sample` says how many reports were
  dropped before it.
- **A connection is one epoch.** `Conn` doesn't reconnect. When it ends,
  `Done` closes, `Err` reports the cause, and you dial again.
- **Measurements stay in the DVL's frame.** X forward, Y starboard, Z down,
  rotated by the device's mounting yaw offset if one is configured. Mounting
  pitch and roll are yours to handle.

## Configuring the device

`Config` reads the device's settings and `UpdateConfig` changes only the
fields you set. A successful update means the device accepted it, so read the
configuration back if you need proof it took effect:

```go
speed := 1480.0
if err := conn.UpdateConfig(ctx, dvl.ConfigUpdate{SpeedOfSound: &speed}); err != nil {
	return err
}
after, err := conn.Config(ctx)
```

The CLI does the read-back for you:

```console
waterlinked-dvl config get
waterlinked-dvl config set -speed-of-sound 1480
```

`ResetDeadReckoning` zeroes the local position frame and `CalibrateGyro`
calibrates the gyroscope, which needs the DVL stationary for up to 15 seconds.

## Documentation

- [Package reference on pkg.go.dev][go-reference] covers connecting, the
  report streams, lock, frames and units, timestamps, errors, and firmware
  compatibility, with runnable examples beside the API they demonstrate.
- [CLI reference][cli-reference] documents every `waterlinked-dvl` command.
- Water Linked's [TCP JSON API][wl-protocol], [axes][wl-axes], and
  [integration guide][wl-integration] describe the device side.

## Development

Standard Go tooling, nothing else. CI runs the race-enabled tests, `gofmt`,
and `go vet` on Go 1.25 and the current stable release, and release-please
cuts releases from conventional commit messages. To preview the package
documentation as pkg.go.dev will render it:

```console
go run golang.org/x/pkgsite/cmd/pkgsite@latest -open .
```

## Stability

Pre-1.0. The public API may change while the package is validated against
production installations. Changes are recorded in the release notes.

## Licence

Apache-2.0. See [LICENSE](LICENSE).

[ci]: https://github.com/infin-robotics/waterlinked-dvl/actions/workflows/ci.yml
[ci-badge]: https://github.com/infin-robotics/waterlinked-dvl/actions/workflows/ci.yml/badge.svg
[cli-reference]: https://pkg.go.dev/github.com/infin-robotics/waterlinked-dvl/cmd/waterlinked-dvl
[go-reference]: https://pkg.go.dev/github.com/infin-robotics/waterlinked-dvl
[license]: LICENSE
[license-badge]: https://img.shields.io/badge/license-Apache--2.0-blue.svg
[reference-badge]: https://pkg.go.dev/badge/github.com/infin-robotics/waterlinked-dvl.svg
[wl-axes]: https://docs.waterlinked.com/dvl/axes/
[wl-integration]: https://docs.waterlinked.com/dvl/integration/
[wl-protocol]: https://docs.waterlinked.com/dvl/dvl-json-protocol/
