package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"net"

	dvl "github.com/infin-robotics/waterlinked-dvl"
)

func runInfo(ctx context.Context, args []string, output commandIO) error {
	flags := newCommandFlags(
		"waterlinked-dvl info",
		"Show identity, firmware, protocol version, and readiness reported by the DVL.",
		output,
	)
	if err := flags.parse(args); err != nil {
		return err
	}

	conn, err := flags.dial(ctx)
	if err != nil {
		return err
	}
	defer conn.Close()

	commandCtx, cancel := commandContext(ctx)
	defer cancel()
	info, err := conn.Info(commandCtx)
	if err != nil {
		return fmt.Errorf("read device info: %w", err)
	}
	return writeInfo(output.stdout, *flags.json, info)
}

func runConfigGet(ctx context.Context, args []string, output commandIO) error {
	flags := newCommandFlags(
		"waterlinked-dvl config get",
		"Show the complete configuration reported by the DVL.",
		output,
	)
	if err := flags.parse(args); err != nil {
		return err
	}

	conn, err := flags.dial(ctx)
	if err != nil {
		return err
	}
	defer conn.Close()

	commandCtx, cancel := commandContext(ctx)
	defer cancel()
	config, err := conn.Config(commandCtx)
	if err != nil {
		return fmt.Errorf("read configuration: %w", err)
	}
	return writeConfig(output.stdout, *flags.json, config)
}

func runConfigSet(ctx context.Context, args []string, output commandIO) error {
	flags := newCommandFlags(
		"waterlinked-dvl config set",
		"Apply only explicitly supplied fields, then read the configuration back and verify them.",
		output,
	)

	speedOfSound := flags.set.Float64("speed-of-sound", 0, "speed of sound in metres per second")
	mountingYaw := flags.set.Float64("mounting-yaw", 0, "clockwise mounting yaw offset in degrees")
	acousticEnabled := flags.set.Bool("acoustic-enabled", false, "enable or disable the acoustic transducers; use =false to disable")
	darkModeEnabled := flags.set.Bool("dark-mode-enabled", false, "enable or disable dark mode; use =false to disable")
	rangeMode := flags.set.String("range-mode", "", `range mode, such as "auto", "wt", "=1", or "1<=3"`)
	periodicCyclingEnabled := flags.set.Bool("periodic-cycling-enabled", false, "enable or disable periodic cycling; use =false to disable")

	if err := flags.parse(args); err != nil {
		return err
	}

	var update dvl.ConfigUpdate
	selected := make([]string, 0, 6)
	flags.set.Visit(func(option *flag.Flag) {
		switch option.Name {
		case "speed-of-sound":
			update.SpeedOfSound = speedOfSound
			selected = append(selected, option.Name)
		case "mounting-yaw":
			update.MountingYawOffset = mountingYaw
			selected = append(selected, option.Name)
		case "acoustic-enabled":
			update.AcousticEnabled = acousticEnabled
			selected = append(selected, option.Name)
		case "dark-mode-enabled":
			update.DarkModeEnabled = darkModeEnabled
			selected = append(selected, option.Name)
		case "range-mode":
			value := dvl.RangeMode(*rangeMode)
			update.RangeMode = &value
			selected = append(selected, option.Name)
		case "periodic-cycling-enabled":
			update.PeriodicCyclingEnabled = periodicCyclingEnabled
			selected = append(selected, option.Name)
		}
	})
	if len(selected) == 0 {
		return errors.New("config set: no configuration values supplied")
	}

	conn, err := flags.dial(ctx)
	if err != nil {
		return err
	}
	defer conn.Close()

	commandCtx, cancel := commandContext(ctx)
	defer cancel()
	before, err := conn.Config(commandCtx)
	if err != nil {
		return fmt.Errorf("read configuration before update: %w", err)
	}
	if err := conn.UpdateConfig(commandCtx, update); err != nil {
		return fmt.Errorf("update configuration: %w", err)
	}
	after, err := conn.Config(commandCtx)
	if err != nil {
		return fmt.Errorf("configuration update accepted but readback failed: %w", err)
	}
	if err := verifyConfigUpdate(update, after); err != nil {
		return fmt.Errorf("configuration update accepted but not observed: %w", err)
	}

	return writeConfigUpdate(output.stdout, *flags.json, selected, before, after)
}

func verifyConfigUpdate(update dvl.ConfigUpdate, actual dvl.Config) error {
	switch {
	case update.SpeedOfSound != nil && actual.SpeedOfSound != *update.SpeedOfSound:
		return fmt.Errorf("speed of sound is %g, want %g", actual.SpeedOfSound, *update.SpeedOfSound)
	case update.MountingYawOffset != nil && actual.MountingYawOffset != *update.MountingYawOffset:
		return fmt.Errorf("mounting yaw is %g, want %g", actual.MountingYawOffset, *update.MountingYawOffset)
	case update.AcousticEnabled != nil && actual.AcousticEnabled != *update.AcousticEnabled:
		return fmt.Errorf("acoustic enabled is %t, want %t", actual.AcousticEnabled, *update.AcousticEnabled)
	case update.DarkModeEnabled != nil && actual.DarkModeEnabled != *update.DarkModeEnabled:
		return fmt.Errorf("dark mode enabled is %t, want %t", actual.DarkModeEnabled, *update.DarkModeEnabled)
	case update.RangeMode != nil && actual.RangeMode != *update.RangeMode:
		return fmt.Errorf("range mode is %q, want %q", actual.RangeMode, *update.RangeMode)
	case update.PeriodicCyclingEnabled != nil && actual.PeriodicCyclingEnabled != *update.PeriodicCyclingEnabled:
		return fmt.Errorf("periodic cycling enabled is %t, want %t", actual.PeriodicCyclingEnabled, *update.PeriodicCyclingEnabled)
	default:
		return nil
	}
}

func runWatch(ctx context.Context, args []string, output commandIO) error {
	flags := newCommandFlags(
		"waterlinked-dvl watch",
		"Print delivered velocity and dead-reckoning reports plus unhandled frames until interrupted.",
		output,
	)
	duration := flags.set.Duration("duration", 0, "stop after this duration; zero waits until interrupted")
	if err := flags.parse(args); err != nil {
		return err
	}
	if *duration < 0 {
		return errors.New("watch duration must not be negative")
	}

	conn, err := flags.dial(ctx)
	if err != nil {
		return err
	}
	defer conn.Close()

	watchCtx := ctx
	cancel := func() {}
	if *duration != 0 {
		watchCtx, cancel = context.WithTimeout(ctx, *duration)
	}
	defer cancel()

	velocity := conn.VelocityReports()
	deadReckoning := conn.DeadReckoningReports()
	unhandled := conn.UnhandledFrames()
	for {
		select {
		case sample, ok := <-velocity:
			if !ok {
				velocity = nil
				continue
			}
			if err := writeVelocity(output.stdout, *flags.json, sample); err != nil {
				return err
			}
		case sample, ok := <-deadReckoning:
			if !ok {
				deadReckoning = nil
				continue
			}
			if err := writeDeadReckoning(output.stdout, *flags.json, sample); err != nil {
				return err
			}
		case sample, ok := <-unhandled:
			if !ok {
				unhandled = nil
				continue
			}
			if err := writeUnhandled(output.stdout, *flags.json, sample); err != nil {
				return err
			}
		case <-watchCtx.Done():
			if ctx.Err() != nil || errors.Is(watchCtx.Err(), context.DeadlineExceeded) {
				return nil
			}
			return watchCtx.Err()
		case <-conn.Done():
			if err := conn.Err(); err != nil && !errors.Is(err, net.ErrClosed) {
				return fmt.Errorf("connection ended: %w", err)
			}
			return nil
		}
	}
}

func runResetDeadReckoning(ctx context.Context, args []string, output commandIO) error {
	return runAcceptedCommand(
		ctx,
		args,
		output,
		"waterlinked-dvl reset-dead-reckoning",
		"Reset the DVL's local dead-reckoning origin and attitude.",
		"reset-dead-reckoning",
		func(ctx context.Context, conn *dvl.Conn) error {
			return conn.ResetDeadReckoning(ctx)
		},
	)
}

func runCalibrateGyro(ctx context.Context, args []string, output commandIO) error {
	return runAcceptedCommand(
		ctx,
		args,
		output,
		"waterlinked-dvl calibrate-gyro",
		"Calibrate the gyroscope. Keep the DVL stationary for the full operation, which may take 15 seconds.",
		"calibrate-gyro",
		func(ctx context.Context, conn *dvl.Conn) error {
			return conn.CalibrateGyro(ctx)
		},
	)
}

func runAcceptedCommand(
	ctx context.Context,
	args []string,
	output commandIO,
	name string,
	summary string,
	resultName string,
	operation func(context.Context, *dvl.Conn) error,
) error {
	flags := newCommandFlags(name, summary, output)
	if err := flags.parse(args); err != nil {
		return err
	}

	conn, err := flags.dial(ctx)
	if err != nil {
		return err
	}
	defer conn.Close()

	commandCtx, cancel := commandContext(ctx)
	defer cancel()
	if err := operation(commandCtx, conn); err != nil {
		return fmt.Errorf("%s: %w", resultName, err)
	}
	return writeAccepted(output.stdout, *flags.json, resultName)
}
