package main

import (
	"encoding/json"
	"fmt"
	"io"
	"strings"
	"time"

	dvl "github.com/infin-robotics/waterlinked-dvl"
)

type deviceInfoOutput struct {
	ChipID               string              `json:"chip_id"`
	HardwareRevision     int                 `json:"hardware_revision"`
	ProductID            int                 `json:"product_id"`
	ProductName          string              `json:"product_name"`
	Variant              string              `json:"variant"`
	FirmwareVersion      string              `json:"firmware_version"`
	FirmwareVersionShort string              `json:"firmware_version_short"`
	Ready                bool                `json:"ready"`
	ProtocolVersion      dvl.ProtocolVersion `json:"protocol_version"`
}

type configOutput struct {
	SpeedOfSound           float64       `json:"speed_of_sound_metres_per_second"`
	MountingYawOffset      float64       `json:"mounting_yaw_offset_degrees"`
	AcousticEnabled        bool          `json:"acoustic_enabled"`
	DarkModeEnabled        bool          `json:"dark_mode_enabled"`
	RangeMode              dvl.RangeMode `json:"range_mode"`
	PeriodicCyclingEnabled bool          `json:"periodic_cycling_enabled"`
}

type configUpdateOutput struct {
	Accepted      bool         `json:"accepted"`
	Observed      bool         `json:"observed"`
	UpdatedFields []string     `json:"updated_fields"`
	Before        configOutput `json:"before"`
	After         configOutput `json:"after"`
}

type vectorOutput struct {
	X float64 `json:"x"`
	Y float64 `json:"y"`
	Z float64 `json:"z"`
}

type anglesOutput struct {
	Roll  float64 `json:"roll_degrees"`
	Pitch float64 `json:"pitch_degrees"`
	Yaw   float64 `json:"yaw_degrees"`
}

type transducerOutput struct {
	ID        uint8   `json:"id"`
	Velocity  float64 `json:"velocity_metres_per_second"`
	Distance  float64 `json:"distance_metres"`
	RSSI      float64 `json:"rssi_dbm"`
	NSD       float64 `json:"nsd_dbm"`
	BeamValid bool    `json:"beam_valid"`
}

type velocityOutput struct {
	Reference            dvl.VelocityReference      `json:"reference"`
	IntervalMilliseconds float64                    `json:"interval_milliseconds"`
	Measurement          *velocityMeasurementOutput `json:"measurement"`
	Status               uint8                      `json:"status"`
	ValidAt              time.Time                  `json:"valid_at"`
	TransmittedAt        time.Time                  `json:"transmitted_at"`
	Transducers          []transducerOutput         `json:"transducers"`
	ProtocolVersion      dvl.ProtocolVersion        `json:"protocol_version"`
}

type velocityMeasurementOutput struct {
	Velocity       vectorOutput `json:"velocity_metres_per_second"`
	FigureOfMerit  float64      `json:"figure_of_merit_metres_per_second"`
	Covariance     dvl.Matrix3  `json:"covariance_metres_squared_per_second_squared"`
	AltitudeMetres *float64     `json:"altitude_metres"`
}

type deadReckoningOutput struct {
	At                                time.Time               `json:"at"`
	PositionMetres                    vectorOutput            `json:"position_metres"`
	Attitude                          anglesOutput            `json:"attitude"`
	HorizontalStandardDeviationMetres float64                 `json:"horizontal_standard_deviation_metres"`
	Status                            dvl.DeadReckoningStatus `json:"status"`
	ProtocolVersion                   dvl.ProtocolVersion     `json:"protocol_version"`
}

type unhandledOutput struct {
	Type            string              `json:"type"`
	ProtocolVersion dvl.ProtocolVersion `json:"protocol_version"`
	Raw             string              `json:"raw"`
	Error           string              `json:"error,omitempty"`
}

type reportEvent struct {
	Type          string `json:"type"`
	DroppedBefore uint64 `json:"dropped_before"`
	Report        any    `json:"report"`
}

func writeInfo(w io.Writer, asJSON bool, info dvl.DeviceInfo) error {
	value := deviceInfoOutput{
		ChipID:               info.ChipID,
		HardwareRevision:     info.HardwareRevision,
		ProductID:            info.ProductID,
		ProductName:          info.ProductName,
		Variant:              info.Variant,
		FirmwareVersion:      info.FirmwareVersion,
		FirmwareVersionShort: info.FirmwareVersionShort,
		Ready:                info.Ready,
		ProtocolVersion:      info.ProtocolVersion,
	}
	if asJSON {
		return writeJSON(w, value, true)
	}

	_, err := fmt.Fprintf(w, `Product:           %s
Variant:           %s
Firmware:          %s
Hardware revision: %d
Product ID:        %d
Chip ID:           %s
Protocol:          %s
Ready:             %s
`, info.ProductName, info.Variant, info.FirmwareVersion, info.HardwareRevision,
		info.ProductID, info.ChipID, info.ProtocolVersion, yesNo(info.Ready))
	return err
}

func writeConfig(w io.Writer, asJSON bool, config dvl.Config) error {
	if asJSON {
		return writeJSON(w, configValue(config), true)
	}

	_, err := fmt.Fprintf(w, `Speed of sound:    %g m/s
Mounting yaw:      %g°
Acoustic enabled:  %s
Dark mode:         %s
Range mode:        %s
Periodic cycling:  %s
`, config.SpeedOfSound, config.MountingYawOffset, yesNo(config.AcousticEnabled),
		yesNo(config.DarkModeEnabled), config.RangeMode, yesNo(config.PeriodicCyclingEnabled))
	return err
}

func writeConfigUpdate(w io.Writer, asJSON bool, selected []string, before, after dvl.Config) error {
	if asJSON {
		return writeJSON(w, configUpdateOutput{
			Accepted:      true,
			Observed:      true,
			UpdatedFields: selected,
			Before:        configValue(before),
			After:         configValue(after),
		}, true)
	}

	var text strings.Builder
	text.WriteString("Configuration accepted and observed.\n")
	for _, name := range selected {
		switch name {
		case "speed-of-sound":
			fmt.Fprintf(&text, "Speed of sound:          %g -> %g m/s\n", before.SpeedOfSound, after.SpeedOfSound)
		case "mounting-yaw":
			fmt.Fprintf(&text, "Mounting yaw:            %g -> %g°\n", before.MountingYawOffset, after.MountingYawOffset)
		case "acoustic-enabled":
			fmt.Fprintf(&text, "Acoustic enabled:        %t -> %t\n", before.AcousticEnabled, after.AcousticEnabled)
		case "dark-mode-enabled":
			fmt.Fprintf(&text, "Dark mode enabled:       %t -> %t\n", before.DarkModeEnabled, after.DarkModeEnabled)
		case "range-mode":
			fmt.Fprintf(&text, "Range mode:              %s -> %s\n", before.RangeMode, after.RangeMode)
		case "periodic-cycling-enabled":
			fmt.Fprintf(&text, "Periodic cycling enabled: %t -> %t\n", before.PeriodicCyclingEnabled, after.PeriodicCyclingEnabled)
		}
	}
	_, err := io.WriteString(w, text.String())
	return err
}

func writeVelocity(w io.Writer, asJSON bool, sample dvl.Sample[dvl.VelocityReport]) error {
	report := sample.Report
	value := velocityValue(report)
	if asJSON {
		return writeJSON(w, reportEvent{Type: "velocity", DroppedBefore: sample.DroppedBefore, Report: value}, false)
	}

	if report.Measurement == nil {
		_, err := fmt.Fprintf(
			w,
			"%s velocity %-6s valid=false status=%d transducers=%d dropped=%d\n",
			report.TransmittedAt.Format(time.RFC3339Nano), report.Reference,
			report.Status, len(report.Transducers), sample.DroppedBefore,
		)
		return err
	}

	measurement := report.Measurement
	altitude := "n/a"
	if measurement.Altitude != nil {
		altitude = fmt.Sprintf("%.3f m", *measurement.Altitude)
	}
	_, err := fmt.Fprintf(
		w,
		"%s velocity %-6s valid=true x=% .3f y=% .3f z=% .3f m/s altitude=%s fom=%.3f dropped=%d\n",
		report.TransmittedAt.Format(time.RFC3339Nano), report.Reference,
		measurement.Velocity.X, measurement.Velocity.Y, measurement.Velocity.Z, altitude,
		measurement.FigureOfMerit, sample.DroppedBefore,
	)
	return err
}

func writeDeadReckoning(w io.Writer, asJSON bool, sample dvl.Sample[dvl.DeadReckoningReport]) error {
	report := sample.Report
	value := deadReckoningValue(report)
	if asJSON {
		return writeJSON(w, reportEvent{Type: "dead_reckoning", DroppedBefore: sample.DroppedBefore, Report: value}, false)
	}

	_, err := fmt.Fprintf(
		w,
		"%s dead-reckoning x=% .3f y=% .3f z=% .3f m roll=% .2f pitch=% .2f yaw=% .2f° status=%d dropped=%d\n",
		report.At.Format(time.RFC3339Nano), report.Position.X, report.Position.Y, report.Position.Z,
		report.Attitude.Roll, report.Attitude.Pitch, report.Attitude.Yaw,
		report.Status, sample.DroppedBefore,
	)
	return err
}

func writeUnhandled(w io.Writer, asJSON bool, sample dvl.Sample[dvl.UnhandledFrame]) error {
	report := sample.Report
	value := unhandledOutput{
		Type:            report.Type,
		ProtocolVersion: report.ProtocolVersion,
		Raw:             string(report.Raw),
	}
	if report.Err != nil {
		value.Error = report.Err.Error()
	}
	if asJSON {
		return writeJSON(w, reportEvent{Type: "unhandled", DroppedBefore: sample.DroppedBefore, Report: value}, false)
	}

	_, err := fmt.Fprintf(w, "unhandled frame type=%q format=%s dropped=%d error=%q raw=%q\n",
		report.Type, report.ProtocolVersion, sample.DroppedBefore, value.Error, value.Raw)
	return err
}

func writeAccepted(w io.Writer, asJSON bool, command string) error {
	if asJSON {
		return writeJSON(w, struct {
			Command  string `json:"command"`
			Accepted bool   `json:"accepted"`
		}{Command: command, Accepted: true}, true)
	}
	_, err := fmt.Fprintf(w, "%s accepted.\n", command)
	return err
}

func configValue(config dvl.Config) configOutput {
	return configOutput{
		SpeedOfSound:           config.SpeedOfSound,
		MountingYawOffset:      config.MountingYawOffset,
		AcousticEnabled:        config.AcousticEnabled,
		DarkModeEnabled:        config.DarkModeEnabled,
		RangeMode:              config.RangeMode,
		PeriodicCyclingEnabled: config.PeriodicCyclingEnabled,
	}
}

func velocityValue(report dvl.VelocityReport) velocityOutput {
	transducers := make([]transducerOutput, len(report.Transducers))
	for index, transducer := range report.Transducers {
		transducers[index] = transducerOutput{
			ID: transducer.ID, Velocity: transducer.Velocity, Distance: transducer.Distance,
			RSSI: transducer.RSSI, NSD: transducer.NSD, BeamValid: transducer.BeamValid,
		}
	}
	value := velocityOutput{
		Reference: report.Reference, IntervalMilliseconds: float64(report.Interval) / float64(time.Millisecond),
		Status: uint8(report.Status), ValidAt: report.ValidAt, TransmittedAt: report.TransmittedAt,
		Transducers: transducers, ProtocolVersion: report.ProtocolVersion,
	}
	if report.Measurement != nil {
		measurement := report.Measurement
		value.Measurement = &velocityMeasurementOutput{
			Velocity:       vectorOutput{X: measurement.Velocity.X, Y: measurement.Velocity.Y, Z: measurement.Velocity.Z},
			FigureOfMerit:  measurement.FigureOfMerit,
			Covariance:     measurement.Covariance,
			AltitudeMetres: measurement.Altitude,
		}
	}
	return value
}

func deadReckoningValue(report dvl.DeadReckoningReport) deadReckoningOutput {
	return deadReckoningOutput{
		At:                                report.At,
		PositionMetres:                    vectorOutput{X: report.Position.X, Y: report.Position.Y, Z: report.Position.Z},
		Attitude:                          anglesOutput{Roll: report.Attitude.Roll, Pitch: report.Attitude.Pitch, Yaw: report.Attitude.Yaw},
		HorizontalStandardDeviationMetres: report.HorizontalStandardDeviation,
		Status:                            report.Status, ProtocolVersion: report.ProtocolVersion,
	}
}

func writeJSON(w io.Writer, value any, indent bool) error {
	encoder := json.NewEncoder(w)
	if indent {
		encoder.SetIndent("", "  ")
	}
	return encoder.Encode(value)
}

func yesNo(value bool) string {
	if value {
		return "yes"
	}
	return "no"
}
