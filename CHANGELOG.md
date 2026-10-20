# Changelog

## [1.0.0](https://github.com/infin-robotics/waterlinked-dvl/compare/v0.1.0...v1.0.0) (2026-10-20)


### ⚠ BREAKING CHANGES

* consumers must replace github.com/sunfish-robotics/waterlinked-dvl imports and module requirements with github.com/infin-robotics/waterlinked-dvl.

### Code Refactoring

* migrate module to infin-robotics ([7a4f8be](https://github.com/infin-robotics/waterlinked-dvl/commit/7a4f8bea8aad20d59620e5a58744d34655cc88be))

## 0.1.0 (2026-09-13)

Initial public release of the Go client and diagnostic CLI for the Water Linked DVL A50 and A125 TCP JSON API.

### Go client

- Decodes bottom- and water-relative velocity and local dead-reckoning reports into typed Go values while preserving device timestamps, covariance, status, and per-transducer diagnostics.
- Represents a velocity report without lock as a nil `Measurement`, preventing stale velocity, covariance, and altitude values from being consumed as current measurements.
- Multiplexes reports and commands over one TCP connection. Independent bounded streams drop their oldest items under backpressure and report the loss through `Sample.DroppedBefore`.
- Exposes explicit connection lifecycle and supervision through `Dialer`, `Done`, `Err`, and `Close`, including configurable idle and command timeouts.
- Supports device identity, complete configuration reads, validated partial configuration updates, dead-reckoning reset, and gyroscope calibration.
- Preserves unknown and malformed frames on `UnhandledFrames` when doing so does not make command correlation ambiguous.

### Diagnostic CLI

- Adds `waterlinked-dvl` commands for identity, configuration, report capture, dead-reckoning reset, and gyroscope calibration.
- Supports human-readable output and machine-readable JSON, with configuration readback and per-stream loss counts.

### Compatibility

The client targets A50 and A125 devices running firmware 2.4.0 or later and the backwards-compatible `json_v3` protocol family from `json_v3.1` onwards. `Info` requires firmware 2.7.2. Live validation covered an A50 running firmware 2.7.2; the A125, water tracking, and dead-reckoning report capture were not exercised on hardware before this release.
