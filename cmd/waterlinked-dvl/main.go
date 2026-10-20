package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"net"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"syscall"
	"time"

	dvl "github.com/infin-robotics/waterlinked-dvl"
)

const (
	defaultAddress = "192.168.194.95"
	dialTimeout    = 10 * time.Second
	commandTimeout = 30 * time.Second
)

type commandIO struct {
	stdout io.Writer
	stderr io.Writer
}

type commandFlags struct {
	set     *flag.FlagSet
	address *string
	json    *bool
	output  commandIO
}

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	err := run(ctx, os.Args[1:], commandIO{stdout: os.Stdout, stderr: os.Stderr})
	if err == nil || errors.Is(err, flag.ErrHelp) {
		return
	}

	fmt.Fprintf(os.Stderr, "waterlinked-dvl: %v\n", err)
	os.Exit(1)
}

func run(ctx context.Context, args []string, output commandIO) error {
	if len(args) == 0 {
		printUsage(output.stderr)
		return errors.New("missing command")
	}

	switch args[0] {
	case "info":
		return runInfo(ctx, args[1:], output)
	case "watch":
		return runWatch(ctx, args[1:], output)
	case "config":
		return runConfig(ctx, args[1:], output)
	case "reset-dead-reckoning":
		return runResetDeadReckoning(ctx, args[1:], output)
	case "calibrate-gyro":
		return runCalibrateGyro(ctx, args[1:], output)
	case "help", "-h", "--help":
		printUsage(output.stdout)
		return nil
	default:
		printUsage(output.stderr)
		return fmt.Errorf("unknown command %q", args[0])
	}
}

func runConfig(ctx context.Context, args []string, output commandIO) error {
	if len(args) == 0 {
		printConfigUsage(output.stderr)
		return errors.New("missing config command")
	}

	switch args[0] {
	case "get":
		return runConfigGet(ctx, args[1:], output)
	case "set":
		return runConfigSet(ctx, args[1:], output)
	case "help", "-h", "--help":
		printConfigUsage(output.stdout)
		return nil
	default:
		printConfigUsage(output.stderr)
		return fmt.Errorf("unknown config command %q", args[0])
	}
}

func newCommandFlags(name, summary string, output commandIO) commandFlags {
	set := flag.NewFlagSet(name, flag.ContinueOnError)
	set.SetOutput(io.Discard)

	flags := commandFlags{
		set:     set,
		address: set.String("address", defaultAddress, "DVL address as HOST or HOST:PORT"),
		json:    set.Bool("json", false, "write machine-readable JSON to stdout"),
		output:  output,
	}
	set.Usage = func() {
		fmt.Fprintf(output.stdout, "Usage: %s [options]\n\n%s\n\nOptions:\n", name, summary)
		set.SetOutput(output.stdout)
		set.PrintDefaults()
		set.SetOutput(io.Discard)
	}
	return flags
}

func (f commandFlags) parse(args []string) error {
	if err := f.set.Parse(args); err != nil {
		return err
	}
	if f.set.NArg() != 0 {
		return fmt.Errorf("%s: unexpected argument %q", f.set.Name(), f.set.Arg(0))
	}
	return nil
}

func (f commandFlags) dial(ctx context.Context) (*dvl.Conn, error) {
	address, err := normaliseAddress(*f.address)
	if err != nil {
		return nil, err
	}

	dialCtx, cancel := context.WithTimeout(ctx, dialTimeout)
	defer cancel()
	return dvl.Dial(dialCtx, address)
}

func commandContext(ctx context.Context) (context.Context, context.CancelFunc) {
	return context.WithTimeout(ctx, commandTimeout)
}

func normaliseAddress(address string) (string, error) {
	address = strings.TrimSpace(address)
	if address == "" {
		return "", errors.New("address must not be empty")
	}
	if _, _, err := net.SplitHostPort(address); err == nil {
		return address, nil
	}

	unbracketed := strings.TrimPrefix(strings.TrimSuffix(address, "]"), "[")
	if ip := net.ParseIP(unbracketed); ip != nil {
		return net.JoinHostPort(ip.String(), strconv.Itoa(dvl.DefaultPort)), nil
	}
	if strings.Contains(address, ":") {
		return "", fmt.Errorf("invalid DVL address %q", address)
	}
	return net.JoinHostPort(address, strconv.Itoa(dvl.DefaultPort)), nil
}

func printUsage(w io.Writer) {
	fmt.Fprintln(w, `Usage: waterlinked-dvl COMMAND [options]

Inspect and operate a Water Linked A50/A125 through the exported Go API.

Commands:
  info                    Show device identity, firmware, and readiness
  config get              Show the complete device configuration
  config set              Apply and verify selected configuration fields
  watch                   Print delivered reports and unhandled frames
  reset-dead-reckoning    Reset the local dead-reckoning frame
  calibrate-gyro          Calibrate the gyroscope while the DVL is stationary

Run "waterlinked-dvl COMMAND -h" for command-specific options.`)
}

func printConfigUsage(w io.Writer) {
	fmt.Fprintln(w, `Usage: waterlinked-dvl config COMMAND [options]

Commands:
  get    Show the complete device configuration
  set    Apply and verify selected configuration fields`)
}
