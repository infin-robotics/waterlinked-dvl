package dvl_test

import (
	"errors"
	"testing"

	dvl "github.com/infin-robotics/waterlinked-dvl"
)

func TestStatusHas(t *testing.T) {
	t.Parallel()

	status := dvl.StatusHighTemperature | dvl.Status(1<<7)
	if !status.Has(dvl.StatusHighTemperature) {
		t.Fatal("high-temperature flag not found")
	}
	if status.Has(dvl.Status(1 << 3)) {
		t.Fatal("unset flag reported as present")
	}
}

func TestProtocolErrorUnwrapsCause(t *testing.T) {
	t.Parallel()

	cause := errors.New("invalid covariance")
	err := &dvl.ProtocolError{
		Operation:   "decode",
		MessageType: "velocity",
		Err:         cause,
	}

	if !errors.Is(err, cause) {
		t.Fatalf("ProtocolError does not retain its cause: %v", err)
	}
}

func TestCommandErrorIncludesDeviceMessage(t *testing.T) {
	t.Parallel()

	err := (&dvl.CommandError{
		Command: "set_config",
		Message: "speed_of_sound out of range",
	}).Error()
	want := `dvl: command "set_config" rejected: speed_of_sound out of range`
	if err != want {
		t.Fatalf("CommandError.Error() = %q, want %q", err, want)
	}
}
