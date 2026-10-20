package dvl

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"sync"
	"time"
)

const (
	// DefaultPort is the TCP port used by the A50/A125 JSON protocol.
	DefaultPort = 16171

	maximumFrameSize = 1 << 20

	defaultReportBuffer   = 64
	defaultCommandTimeout = 30 * time.Second
)

var errNilContext = errors.New("dvl: nil context")

// Conn is one TCP connection epoch with a Water Linked DVL.
//
// A Conn does not reconnect. Callers replace it after the connection ends.
// Command methods may be called concurrently; the connection serialises them
// because protocol responses do not carry unique request IDs. Close may be
// called concurrently with any operation. If a command is cancelled after
// transmission, its response is retired before the next command begins so
// correlation remains unambiguous.
//
// Besides an explicit Close, a connection ends on: a transport error or EOF; a
// frame over the size cap; a failed command write; a command whose response
// has not arrived within the dialer's command timeout; no complete frame
// within the dialer's idle timeout, when one is set; or a response naming a
// command other than the one in flight. A command left unanswered ends the
// connection because a response that arrives after the wait could no longer
// be told apart from the response to the command issued after it. Everything
// else the device sends that the package cannot decode is reported on
// UnhandledFrames instead, except a response that arrives while its command
// is still waiting: an undecodable one fails that command's own call and
// never reaches this stream.
type Conn struct {
	state *connState
}

type connState struct {
	socket net.Conn

	// Dialer settings, resolved to their defaults once at dial time.
	idleTimeout    time.Duration
	commandTimeout time.Duration

	velocity      reportStream[VelocityReport]
	deadReckoning reportStream[DeadReckoningReport]
	unhandled     reportStream[UnhandledFrame]
	commands      chan commandRequest
	workers       sync.WaitGroup

	// ctx is cancelled when the connection ends; cancel may be called more
	// than once, but only the first call sets the cause. context.Cause(ctx)
	// is the terminal error.
	ctx    context.Context
	cancel context.CancelCauseFunc

	pendingMu sync.Mutex
	pending   *pendingCommand
}

type commandRequest struct {
	ctx      context.Context
	name     string
	payload  []byte
	validate func(commandResponse) error
	complete chan commandResult
}

type commandResult struct {
	response commandResponse
	err      error
}

type pendingCommand struct {
	name string

	// outcome carries either the correlated response or the decoding error that
	// stopped the reader from producing one. The reader fills it at most once:
	// it blocks on processed afterwards, and finishCommand retires the slot
	// before closing that channel.
	outcome   chan commandResult
	processed chan struct{}
}

// Dialer configures how connections are opened and supervised. The zero value
// is ready to use and matches Dial.
type Dialer struct {
	// NetDialer opens the TCP connection. When nil a zero net.Dialer is used,
	// which enables the operating system's TCP keep-alive with Go's defaults.
	NetDialer *net.Dialer

	// IdleTimeout bounds the wait for the next complete frame: the deadline is
	// set once, when the wait begins, and is not extended by bytes that arrive
	// without completing a frame, so a peer trickling partial data can still
	// exceed it. The terminal error wraps the underlying deadline error, so
	// errors.Is(err, os.ErrDeadlineExceeded) reports true. Zero disables the
	// check. A device with acoustics disabled may legitimately send nothing,
	// so choose a value with that in mind.
	IdleTimeout time.Duration

	// CommandTimeout bounds how long a command waits for the device's
	// response. It also bounds the socket write that sends the command. When
	// a command's response has not arrived within this bound, the connection
	// ends with a *ProtocolError wrapping context.DeadlineExceeded, because a
	// response arriving later could be matched to the wrong command. Zero
	// uses 30 seconds.
	CommandTimeout time.Duration

	// ReportBuffer is the number of items each stream retains for a slow
	// consumer before dropping the oldest. Zero uses 64.
	ReportBuffer int
}

// Dial connects to address using d.
//
// address must include a TCP port. A negative field on d is rejected before any
// network activity. Cancelling ctx while dialing returns an error comparable
// with ctx.Err; once Dial succeeds, cancelling ctx has no effect on the
// returned connection. Dial resolves d's zero fields to their defaults for this
// connection and never writes to d, so one Dialer may open many connections.
func (d *Dialer) Dial(ctx context.Context, address string) (*Conn, error) {
	if ctx == nil {
		return nil, errNilContext
	}
	if d.IdleTimeout < 0 {
		return nil, fmt.Errorf("dvl: negative IdleTimeout %s", d.IdleTimeout)
	}
	if d.CommandTimeout < 0 {
		return nil, fmt.Errorf("dvl: negative CommandTimeout %s", d.CommandTimeout)
	}
	if d.ReportBuffer < 0 {
		return nil, fmt.Errorf("dvl: negative ReportBuffer %d", d.ReportBuffer)
	}

	netDialer := d.NetDialer
	if netDialer == nil {
		netDialer = &net.Dialer{}
	}
	commandTimeout := d.CommandTimeout
	if commandTimeout == 0 {
		commandTimeout = defaultCommandTimeout
	}
	reportBuffer := d.ReportBuffer
	if reportBuffer == 0 {
		reportBuffer = defaultReportBuffer
	}

	socket, err := netDialer.DialContext(ctx, "tcp", address)
	if err != nil {
		return nil, fmt.Errorf("dvl: dial %s: %w", address, err)
	}

	// The connection's lifetime is deliberately rooted in Background rather
	// than ctx: cancelling ctx after Dial returns must not end the connection.
	lifetime, cancel := context.WithCancelCause(context.Background())
	state := &connState{
		socket:         socket,
		idleTimeout:    d.IdleTimeout,
		commandTimeout: commandTimeout,
		velocity:       newReportStream[VelocityReport](reportBuffer),
		deadReckoning:  newReportStream[DeadReckoningReport](reportBuffer),
		unhandled:      newReportStream[UnhandledFrame](reportBuffer),
		commands:       make(chan commandRequest),
		ctx:            lifetime,
		cancel:         cancel,
	}
	conn := &Conn{state: state}

	start := func(worker func()) {
		state.workers.Add(1)
		go func() {
			defer state.workers.Done()
			worker()
		}()
	}
	start(func() { state.velocity.run(state.ctx.Done()) })
	start(func() { state.deadReckoning.run(state.ctx.Done()) })
	start(func() { state.unhandled.run(state.ctx.Done()) })
	start(state.runCommands)
	start(state.readMessages)

	return conn, nil
}

// Dial connects to address with a zero Dialer. Dialer.Dial documents the
// contract the returned connection follows.
func Dial(ctx context.Context, address string) (*Conn, error) {
	return (&Dialer{}).Dial(ctx, address)
}

// VelocityReports returns bottom- and water-relative velocity reports. Repeated
// calls return the same channel; multiple receivers divide the stream rather
// than broadcasting it.
//
// Reports are delivered through a bounded, drop-oldest queue so a slow or
// absent consumer cannot prevent command responses or other report types from
// being read. Each Sample reports velocity-stream loss. The channel closes when
// the connection ends, and buffered reports from that connection epoch are
// discarded.
func (c *Conn) VelocityReports() <-chan Sample[VelocityReport] {
	return c.state.velocity.output
}

// DeadReckoningReports returns local position and orientation reports. Its
// buffering and receiver semantics match VelocityReports, with independent
// loss accounting.
func (c *Conn) DeadReckoningReports() <-chan Sample[DeadReckoningReport] {
	return c.state.deadReckoning.output
}

// UnhandledFrames returns frames that could not be decoded into a typed
// report, including well-formed reports of unknown type. It does not include
// a response that arrives while its command is still waiting: an undecodable
// one instead fails that command's own call and never reaches this stream.
// Its buffering and receiver semantics match VelocityReports, with
// independent loss accounting. Receiving from it is optional; an absent
// consumer never blocks the reader.
func (c *Conn) UnhandledFrames() <-chan Sample[UnhandledFrame] {
	return c.state.unhandled.output
}

// Done is closed when the connection ends. It is a broadcast lifecycle signal
// for supervisors and callers that do not consume report streams.
func (c *Conn) Done() <-chan struct{} {
	return c.state.ctx.Done()
}

// Err returns nil while the connection is active and its terminal cause after
// Done is closed. The cause is stable and is not consumed by reading it.
func (c *Conn) Err() error {
	return context.Cause(c.state.ctx)
}

// Info returns the connected device's identity, firmware version, and
// readiness state.
func (c *Conn) Info(ctx context.Context) (DeviceInfo, error) {
	var info DeviceInfo
	_, err := c.command(ctx, "get_version_info", nil, func(response commandResponse) error {
		var decodeErr error
		info, decodeErr = decodeDeviceInfo(response.Result, response.Format)
		return decodeErr
	})
	if err != nil {
		return DeviceInfo{}, err
	}
	return info, nil
}

// Config returns the complete configuration reported by the device. Values are
// not validated on the way in: the device is the authority on its own state, so
// Config can report a value UpdateConfig would refuse to send.
func (c *Conn) Config(ctx context.Context) (Config, error) {
	var config Config
	_, err := c.command(ctx, "get_config", nil, func(response commandResponse) error {
		var decodeErr error
		config, decodeErr = decodeConfig(response.Result)
		return decodeErr
	})
	if err != nil {
		return Config{}, err
	}
	return config, nil
}

// UpdateConfig applies the non-nil fields in update.
//
// The complete update is validated before anything is written, so a
// read-modify-write round trip can fail here on a value Config reported
// unchanged. A successful response means the device accepted the command;
// callers that need verified application should read Config again and compare
// it with their desired state.
func (c *Conn) UpdateConfig(ctx context.Context, update ConfigUpdate) error {
	parameters, err := encodeConfigUpdate(update)
	if err != nil {
		return err
	}
	_, err = c.command(ctx, "set_config", parameters, nil)
	return err
}

// ResetDeadReckoning resets the origin and attitude of the device's local
// dead-reckoning frame. A successful response means the reset was accepted;
// reports may take approximately 50 milliseconds to reach zero.
func (c *Conn) ResetDeadReckoning(ctx context.Context) error {
	_, err := c.command(ctx, "reset_dead_reckoning", nil, nil)
	return err
}

// CalibrateGyro calibrates the A50/A125 gyroscope. The device must remain
// stationary while calibration is in progress, which may take up to 15
// seconds.
func (c *Conn) CalibrateGyro(ctx context.Context) error {
	_, err := c.command(ctx, "calibrate_gyro", nil, nil)
	return err
}

// Close terminates the connection and unblocks pending operations. It is safe
// to call Close more than once. When Close returns, every report stream is
// closed and the connection owns no running goroutines. Operations interrupted
// by Close return errors comparable with net.ErrClosed; unexpected transport
// failures retain their underlying EOF or network error.
func (c *Conn) Close() error {
	c.state.terminate(net.ErrClosed)
	c.state.workers.Wait()
	return nil
}

func (c *Conn) command(
	ctx context.Context,
	name string,
	parameters map[string]any,
	validate func(commandResponse) error,
) (commandResponse, error) {
	if ctx == nil {
		return commandResponse{}, errNilContext
	}

	message := commandMessage{Command: name, Parameters: parameters}
	payload, err := json.Marshal(message)
	if err != nil {
		return commandResponse{}, fmt.Errorf("dvl: encode command %q: %w", name, err)
	}

	request := commandRequest{
		ctx:      ctx,
		name:     name,
		payload:  payload,
		validate: validate,
		complete: make(chan commandResult, 1),
	}

	select {
	case c.state.commands <- request:
	case <-ctx.Done():
		return commandResponse{}, ctx.Err()
	case <-c.state.ctx.Done():
		return commandResponse{}, c.Err()
	}

	select {
	case result := <-request.complete:
		if result.err != nil {
			return commandResponse{}, result.err
		}
		if !result.response.Success {
			return commandResponse{}, &CommandError{
				Command: result.response.ResponseTo,
				Message: result.response.ErrorMessage,
			}
		}
		return result.response, nil
	case <-ctx.Done():
		return commandResponse{}, ctx.Err()
	}
}

func (s *connState) runCommands() {
	for {
		select {
		case <-s.ctx.Done():
			return
		case request := <-s.commands:
			select {
			case <-s.ctx.Done():
				request.complete <- commandResult{err: context.Cause(s.ctx)}
				return
			default:
			}
			if err := request.ctx.Err(); err != nil {
				request.complete <- commandResult{err: err}
				continue
			}
			s.runCommand(request)
		}
	}
}

func (s *connState) runCommand(request commandRequest) {
	pending := &pendingCommand{
		name:      request.name,
		outcome:   make(chan commandResult, 1),
		processed: make(chan struct{}),
	}
	s.pendingMu.Lock()
	s.pending = pending
	s.pendingMu.Unlock()

	if err := s.writeCommand(request.payload); err != nil {
		s.clearPending(pending)
		terminalErr := fmt.Errorf("dvl: write command %q: %w", request.name, err)
		s.terminate(terminalErr)
		request.complete <- commandResult{err: context.Cause(s.ctx)}
		return
	}

	timer := time.NewTimer(s.commandTimeout)
	defer timer.Stop()

	select {
	case outcome := <-pending.outcome:
		s.finishCommand(request, pending, outcome)
	case <-timer.C:
		select {
		case outcome := <-pending.outcome:
			s.finishCommand(request, pending, outcome)
			return
		default:
		}
		s.clearPending(pending)
		terminalErr := &ProtocolError{
			Operation:   "wait for response",
			MessageType: request.name,
			Err:         context.DeadlineExceeded,
		}
		s.terminate(terminalErr)
		request.complete <- commandResult{err: context.Cause(s.ctx)}
	case <-s.ctx.Done():
		select {
		case outcome := <-pending.outcome:
			s.finishCommand(request, pending, outcome)
			return
		default:
		}
		s.clearPending(pending)
		request.complete <- commandResult{err: context.Cause(s.ctx)}
	}
}

// finishCommand retires the pending slot and hands outcome to the caller. A
// response the caller cannot use fails that one command: an undecodable result
// says nothing about the connection's framing or its correlation, so the
// connection stays up.
func (s *connState) finishCommand(request commandRequest, pending *pendingCommand, outcome commandResult) {
	defer close(pending.processed)
	s.clearPending(pending)
	if outcome.err != nil {
		request.complete <- outcome
		return
	}

	response := outcome.response
	if response.Success && request.validate != nil {
		if err := request.validate(response); err != nil {
			request.complete <- commandResult{err: &ProtocolError{
				Operation:   "decode response",
				MessageType: response.ResponseTo,
				Err:         err,
			}}
			return
		}
	}
	request.complete <- commandResult{response: response}
}

func (s *connState) writeCommand(payload []byte) (err error) {
	if err = s.socket.SetWriteDeadline(time.Now().Add(s.commandTimeout)); err != nil {
		return err
	}
	defer func() {
		if resetErr := s.socket.SetWriteDeadline(time.Time{}); err == nil {
			err = resetErr
		}
	}()

	for len(payload) != 0 {
		var written int
		written, err = s.socket.Write(payload)
		if err != nil {
			return err
		}
		if written == 0 {
			return io.ErrShortWrite
		}
		payload = payload[written:]
	}
	return nil
}

func (s *connState) clearPending(pending *pendingCommand) {
	s.pendingMu.Lock()
	defer s.pendingMu.Unlock()
	if s.pending == pending {
		s.pending = nil
	}
}

func (s *connState) readMessages() {
	scanner := bufio.NewScanner(s.socket)
	scanner.Buffer(make([]byte, 4096), maximumFrameSize)

	for {
		if err := s.armIdleDeadline(); err != nil {
			s.terminate(fmt.Errorf("dvl: read: %w", err))
			return
		}
		if !scanner.Scan() {
			break
		}
		receivedAt := time.Now()

		frame := bytes.TrimSpace(scanner.Bytes())
		if len(frame) == 0 {
			continue
		}

		// decodeMessage resolves every frame into exactly one payload, so a frame
		// we cannot decode is reported rather than fatal. Only framing and
		// command correlation end the connection.
		message := decodeMessage(frame)
		switch {
		case message.response != nil:
			if !s.deliverResponse(frame, *message.response, receivedAt) {
				return
			}
		case message.velocity != nil:
			if !s.velocity.publish(s.ctx.Done(), *message.velocity, receivedAt) {
				return
			}
		case message.deadReckoning != nil:
			if !s.deadReckoning.publish(s.ctx.Done(), *message.deadReckoning, receivedAt) {
				return
			}
		case message.unhandled != nil:
			if !s.reportUnhandled(*message.unhandled, receivedAt) {
				return
			}
		default:
			// Unreachable while decodeMessage sets exactly one payload, which
			// FuzzDecodeMessage guards. If that ever regresses, stop loudly
			// rather than dropping frames on the floor.
			s.terminate(&ProtocolError{
				Operation:   "decode message",
				MessageType: "",
				Err:         errors.New("decoded message has no payload"),
			})
			return
		}
	}

	if err := scanner.Err(); err != nil {
		if s.idleTimeout != 0 && errors.Is(err, os.ErrDeadlineExceeded) {
			s.terminate(fmt.Errorf("dvl: no frame received for %s: %w", s.idleTimeout, err))
			return
		}
		s.terminate(fmt.Errorf("dvl: read: %w", err))
		return
	}
	s.terminate(io.EOF)
}

// armIdleDeadline gives the next scan the configured idle timeout to produce a
// frame. A zero idle timeout leaves the socket without a read deadline, which
// is how it was dialled.
func (s *connState) armIdleDeadline() error {
	if s.idleTimeout == 0 {
		return nil
	}
	return s.socket.SetReadDeadline(time.Now().Add(s.idleTimeout))
}

// reportUnhandled publishes frame on the unhandled stream, except when it is a
// response that a command is still waiting for: that command receives the
// decoding error instead. It reports false when the connection ended.
func (s *connState) reportUnhandled(frame UnhandledFrame, receivedAt time.Time) bool {
	if frame.Type == "response" && s.failPendingCommand(frame.Err) {
		return true
	}
	return s.unhandled.publish(s.ctx.Done(), frame, receivedAt)
}

// failPendingCommand hands err to the command awaiting a response and waits for
// it to retire the pending slot. It reports false when no command was waiting,
// or when the connection ended before the command retired.
func (s *connState) failPendingCommand(err error) bool {
	s.pendingMu.Lock()
	pending := s.pending
	if pending == nil {
		s.pendingMu.Unlock()
		return false
	}
	outcome := pending.outcome
	s.pendingMu.Unlock()

	outcome <- commandResult{err: err}
	select {
	case <-pending.processed:
		return true
	case <-s.ctx.Done():
		return false
	}
}

// deliverResponse routes response to the command awaiting it. A response that
// names a command other than the one in flight makes correlation ambiguous and
// ends the connection; one that arrives with no command pending is reported as
// an unhandled frame, which is also what a late second response to an
// already-retired command looks like. It reports false when the reader must
// stop.
func (s *connState) deliverResponse(frame []byte, response commandResponse, receivedAt time.Time) bool {
	s.pendingMu.Lock()
	pending := s.pending
	if pending == nil {
		s.pendingMu.Unlock()
		return s.unhandled.publish(s.ctx.Done(), *unhandledFrame(
			frame,
			"response",
			response.Format,
			fmt.Errorf("unsolicited response to %q", response.ResponseTo),
		), receivedAt)
	}
	if response.ResponseTo != pending.name {
		expected := pending.name
		s.pendingMu.Unlock()
		s.terminate(&ProtocolError{
			Operation:   "correlate response",
			MessageType: response.ResponseTo,
			Err:         fmt.Errorf("response is for %q, want %q", response.ResponseTo, expected),
		})
		return false
	}
	outcome := pending.outcome
	s.pendingMu.Unlock()

	outcome <- commandResult{response: response}
	select {
	case <-pending.processed:
		return true
	case <-s.ctx.Done():
		return false
	}
}

// terminate ends the connection with err as its terminal cause, mapping a nil
// err to io.EOF, and closes the socket, which is what unblocks a reader
// blocked in Scan. Only the first call sets the cause; later calls leave it
// unchanged but still close the (already closed) socket, so terminate is safe
// to call repeatedly and from any goroutine.
func (s *connState) terminate(err error) {
	if err == nil {
		err = io.EOF
	}
	s.cancel(err)
	_ = s.socket.Close()
}

// CommandError reports a syntactically valid command rejected by the device.
type CommandError struct {
	Command string
	Message string
}

func (e *CommandError) Error() string {
	if e.Message == "" {
		return fmt.Sprintf("dvl: command %q rejected", e.Command)
	}
	return fmt.Sprintf("dvl: command %q rejected: %s", e.Command, e.Message)
}

// ProtocolError reports malformed or incompatible data received from the
// device. Err retains the underlying decoding or validation error.
type ProtocolError struct {
	Operation   string
	MessageType string
	Err         error
}

func (e *ProtocolError) Error() string {
	message := "dvl: protocol error"
	if e.Operation != "" {
		message += " during " + e.Operation
	}
	if e.MessageType != "" {
		message += " for " + e.MessageType
	}
	if e.Err != nil {
		message += ": " + e.Err.Error()
	}
	return message
}

func (e *ProtocolError) Unwrap() error {
	return e.Err
}
