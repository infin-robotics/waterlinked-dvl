package dvl

import (
	"encoding/json"
	"time"
)

// Sample is one item delivered on a typed stream.
type Sample[T any] struct {
	// Report is the delivered item. It is a value, so the caller owns it
	// outright once received.
	Report T

	// DroppedBefore is the number of older items from the same stream discarded
	// since the previous delivered sample because the consumer did not keep up.
	// It is zero when nothing was lost.
	DroppedBefore uint64

	// ReceivedAt is when the connection reader received the complete frame,
	// before decoding and buffering. It retains the host's monotonic clock
	// reading, so in-process ages do not depend on device clock synchronisation
	// or on how long the report waited for its consumer.
	ReceivedAt time.Time
}

// ProtocolVersion is the format identifier carried by a device message. The
// client decodes the backwards-compatible json_v3 family; a report in another
// major version is reported on Conn.UnhandledFrames with a *ProtocolError
// rather than decoded. Responses are decoded whatever format they claim.
type ProtocolVersion string

const (
	// ProtocolJSONV3_3 is the current documented A50/A125 TCP JSON format.
	ProtocolJSONV3_3 ProtocolVersion = "json_v3.3"
)

// VelocityReference identifies what a velocity measurement is relative to.
type VelocityReference string

const (
	// VelocityReferenceBottom is velocity relative to the reflecting bottom.
	VelocityReferenceBottom VelocityReference = "bottom"

	// VelocityReferenceWater is velocity relative to the water column.
	VelocityReferenceWater VelocityReference = "water"
)

// Vector3 is a three-dimensional value in the frame emitted by the DVL: X
// forward, Y starboard, and Z down. When the device has a mounting yaw offset
// configured, the emitted frame is the vehicle frame rather than the DVL's
// body frame; see Config.MountingYawOffset.
type Vector3 struct {
	X float64
	Y float64
	Z float64
}

// EulerAngles is an orientation in degrees: Roll about the X axis, Pitch
// about the Y axis, and Yaw, which is heading, about the Z axis.
type EulerAngles struct {
	Roll  float64
	Pitch float64
	Yaw   float64
}

// Matrix3 is a 3×3 matrix in row-major order. As a covariance, its rows and
// columns are ordered X, Y, Z.
type Matrix3 [3][3]float64

// Status is the raw eight-bit status mask on a velocity report. Unknown bits
// are preserved for newer device firmware.
type Status uint8

const (
	// StatusHighTemperature indicates that the DVL is approaching thermal
	// shutdown.
	StatusHighTemperature Status = 1 << iota
)

// Has reports whether every bit in flag is present.
func (s Status) Has(flag Status) bool {
	return s&flag == flag
}

// TransducerReading contains one beam's measurement and diagnostics.
type TransducerReading struct {
	// ID is the transducer's protocol identifier. Ids are zero-based, and
	// A50/A125 devices report 0-3. Water Linked's mechanical drawings number
	// the same transducers 1-4.
	ID uint8

	// Velocity and Distance are measured in metres per second and metres,
	// along the beam.
	Velocity float64
	Distance float64

	// RSSI and NSD are the received signal strength and noise spectral
	// density, measured in dBm.
	RSSI float64
	NSD  float64

	// BeamValid reports whether the device trusted this beam's reflection.
	BeamValid bool
}

// VelocityReport is one velocity calculation. The fields on the report itself
// are meaningful whether or not the DVL has a lock; the measurement is present
// only when it does.
//
// The device sends one report per ping. The rate depends on altitude and is
// between 2 and 15 Hz in bottom tracking and 2 Hz in water tracking.
type VelocityReport struct {
	// Reference is the surface the velocity is relative to.
	Reference VelocityReference

	// Interval is the elapsed time since the preceding velocity report, as
	// measured by the device.
	Interval time.Duration

	// Measurement is nil when the device reports velocity_valid false. The
	// device still emits the report, with stale or meaningless numbers in the
	// measurement fields, so they are withheld rather than passed through.
	Measurement *VelocityMeasurement

	// Status is the device's status mask; see Status for the known bits.
	Status Status

	// ValidAt is the instant of the surface reflection, which Water Linked
	// calls the centre of ping. TransmittedAt is the instant immediately before
	// the device sent the report. Both are read from the device's clock.
	ValidAt       time.Time
	TransmittedAt time.Time

	// Transducers carries per-beam diagnostics and is populated on every
	// report, which is how loss of lock is diagnosed.
	Transducers []TransducerReading

	// ProtocolVersion is the format the device used for this report.
	ProtocolVersion ProtocolVersion
}

// VelocityMeasurement is the part of a velocity report that is only valid
// while the DVL has a lock on the reflecting surface.
type VelocityMeasurement struct {
	// Velocity is in metres per second along each axis of the emitted frame.
	Velocity Vector3

	// FigureOfMerit is the estimated velocity standard deviation in metres per
	// second. Lower values indicate a more precise estimate.
	FigureOfMerit float64

	// Covariance is the velocity covariance matrix in (m/s)².
	Covariance Matrix3

	// Altitude is the distance to the reflecting surface along the emitted Z
	// axis, in metres. It is nil when the device omits altitude, which is the
	// case in water tracking.
	Altitude *float64
}

// DeadReckoningStatus is the raw status value on a position_local report.
type DeadReckoningStatus uint8

const (
	// DeadReckoningStatusOK means the device reported no dead-reckoning issue.
	DeadReckoningStatusOK DeadReckoningStatus = 0

	// DeadReckoningStatusFault means the device reported an issue. The
	// protocol does not say which.
	DeadReckoningStatusFault DeadReckoningStatus = 1
)

// DeadReckoningReport is the device's local position and orientation estimate.
// Its frame is established at startup or by ResetDeadReckoning. The device
// sends one about every 200 milliseconds.
//
// Between locks the device integrates its inertial sensors alone, so position
// error grows quickly; HorizontalStandardDeviation is how the device reports
// that growth.
type DeadReckoningReport struct {
	// At is the report's timestamp, decoded from the Unix seconds the device
	// reports on its own clock.
	At time.Time

	// Position is the displacement in metres from the dead-reckoning origin.
	Position Vector3

	// Attitude is the orientation relative to the dead-reckoning frame. A reset
	// zeroes all three angles.
	Attitude EulerAngles

	// HorizontalStandardDeviation is the estimated horizontal position error in
	// metres.
	HorizontalStandardDeviation float64

	// Status is the device's dead-reckoning status.
	Status DeadReckoningStatus

	// ProtocolVersion is the format the device used for this report.
	ProtocolVersion ProtocolVersion
}

// UnhandledFrame is a frame the connection received but could not turn into a
// typed report. Err is nil when the frame was a well-formed report of a type
// this version of the package does not model; otherwise it is a *ProtocolError
// describing why decoding failed. Raw is an owned copy of the complete frame;
// it is not guaranteed to be valid JSON when Err is set.
type UnhandledFrame struct {
	// Type is the frame's "type" field, or "" when the frame had none or was
	// not a JSON object.
	Type string

	// ProtocolVersion is the frame's "format" field, or "" when it had none.
	ProtocolVersion ProtocolVersion

	// Raw is the complete frame as received, without its trailing newline.
	Raw json.RawMessage

	// Err explains why the frame could not be decoded, or is nil for a
	// well-formed report of an unmodelled type.
	Err error
}
