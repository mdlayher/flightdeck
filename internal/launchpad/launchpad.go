// Copyright 2020 Matt Layher
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
// http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package launchpad

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strings"
	"sync"

	"gitlab.com/gomidi/midi"
)

// Constants from the MIDI and/or Launchpad-specific protocol.
const (
	noteOn           = 0x90 // Activate an LED.
	threeNoteOn      = 0x92 // Rapid fill LEDs.
	controllerChange = 0xb0 // Control messages and top row of LEDs.
	buttonPressed    = 0x7f // Indicates a button is pressed.

	// The substring used to look for Launchpad MIDI devices.
	name = "Launchpad"
)

// A Color is a bitmask which indicates an LED color combination and/or
// control flags.
type Color byte

// A list of Colors which may be applied on Launchpad devices which only have
// red and green LEDs.
const (
	Off          Color = 0x0
	RedLow       Color = 0x01
	RedMedium    Color = 0x02
	RedHigh      Color = 0x03
	GreenHigh    Color = 0x30
	GreenMedium  Color = 0x20
	GreenLow     Color = 0x10
	OrangeLow    Color = RedMedium | GreenLow
	OrangeMedium Color = RedHigh | GreenLow
	OrangeHigh   Color = RedHigh | GreenMedium
	YellowLow    Color = RedLow | GreenLow
	YellowMedium Color = RedLow | GreenMedium
	YellowHigh   Color = RedHigh | GreenHigh

	// Special flags for double buffering and LED flashing operations.
	Copy  Color = 0x4
	Clear Color = 0x8
)

// ErrDevice indicates that a MIDI input and/or output device is not a
// Novation Launchpad device.
var ErrDevice = errors.New("device is not a launchpad")

// A Device is a Novation Launchpad MIDI device.
type Device struct {
	mu  sync.Mutex
	in  midi.In
	out midi.Out
	wg  sync.WaitGroup
}

// Devices detects and opens handles to all Launchpad devices attached
// to this system. If no Launchpad devices are detected, a nil slice and
// nil error will be returned.
func Devices(drv midi.Driver) ([]*Device, error) {
	inputs, err := drv.Ins()
	if err != nil {
		return nil, fmt.Errorf("failed to get inputs: %w", err)
	}

	outputs, err := drv.Outs()
	if err != nil {
		return nil, fmt.Errorf("failed to get inputs: %w", err)
	}

	// Look for matching input and output devices.
	var devices []*Device
	for _, in := range inputs {
		for _, out := range outputs {
			d, err := Open(in, out)
			if err != nil {
				// Indicates either a mismatch or a non-Launchpad MIDI device.
				if errors.Is(err, ErrDevice) {
					continue
				}

				return nil, err
			}

			devices = append(devices, d)
		}
	}

	if len(devices) == 0 {
		// No devices found, nothing to do.
		return nil, nil
	}

	return devices, nil
}

// Open initializes a Device using MIDI input and output devices. If in and/or out
// are not Novation Launchpad devices, the value ErrDevice can be unwrapped
// from the returned error.
func Open(in midi.In, out midi.Out) (*Device, error) {
	// Try to associate input and outputs for a single Launchpad device.
	//
	// TODO(mdlayher): is there a better way?
	if !strings.Contains(in.String(), name) {
		return nil, fmt.Errorf("input %q: %w", in, ErrDevice)
	}

	if !strings.Contains(out.String(), name) {
		return nil, fmt.Errorf("output %q: %w", in, ErrDevice)
	}

	if in.Number() != out.Number() {
		return nil, fmt.Errorf("device number mismatch: input: %d, output: %d: %w",
			in.Number(), out.Number(), ErrDevice)
	}

	// Now that we've found a valid device, open the input and output channels
	// and reset the device's state to the defaults.
	if err := in.Open(); err != nil {
		return nil, fmt.Errorf("failed to open input: %w", err)
	}

	if err := out.Open(); err != nil {
		return nil, fmt.Errorf("failed to open output: %w", err)
	}

	d := &Device{
		in:  in,
		out: out,
	}

	// Optimistically reset the device but don't return an error if the device
	// indicates EOF, as the test driver devices do.
	if err := d.Reset(); err != nil && err != io.EOF {
		return nil, fmt.Errorf("failed to perform initial reset: %w", err)
	}

	return d, nil
}

// String returns a description of a Device's underlying MIDI devices.
func (d *Device) String() string {
	return fmt.Sprintf("launchpad: input: %q, output: %q", d.in, d.out)
}

// Close closes the input and output MIDI channels for a Device and resets it
// to the default state.
func (d *Device) Close() error {
	d.mu.Lock()
	defer func() {
		d.mu.Unlock()
		d.wg.Wait()
	}()

	// Optimistically reset the device but don't return an error if the device
	// indicates EOF, as the test driver devices do.
	if err := d.resetLocked(); err != nil && err != io.EOF {
		return fmt.Errorf("failed to reset on close: %w", err)
	}

	if err := d.in.Close(); err != nil {
		return fmt.Errorf("failed to close input: %w", err)
	}

	if err := d.out.Close(); err != nil {
		return fmt.Errorf("failed to close output: %w", err)
	}

	return nil
}

// Listen invokes fn for each input MIDI message from a Launchpad device until
// ctx is canceled. The context must be canceled when listening for messages
// is no longer necessary.
//
// Most callers should use Events instead.
func (d *Device) Listen(ctx context.Context, fn func(b []byte, timestamp int64)) error {
	d.mu.Lock()
	defer d.mu.Unlock()

	if err := d.in.SetListener(fn); err != nil {
		return fmt.Errorf("failed to listen for inputs: %w", err)
	}

	// Now that the callback has been applied, we return control to the
	// caller and wait for ctx to be canceled. At that point, we stop listening
	// for events and close the channel to signal the consumer.
	d.wg.Add(1)
	go func() {
		defer func() {
			d.mu.Unlock()
			d.wg.Done()
		}()

		<-ctx.Done()

		// Now that the context is canceled, clean up the listener.
		d.mu.Lock()
		_ = d.in.StopListening()
	}()

	return nil
}

// An Event indicates that a button at coordinates (X, Y) was pressed or
// released.
type Event struct {
	X, Y int
	On   bool
}

// Events immediately opens and returns a channel of input events from a
// Launchpad device. The channel is closed when ctx is canceled. The context
// must be canceled when listening for Events is no longer necessary.
func (d *Device) Events(ctx context.Context) (<-chan Event, error) {
	d.mu.Lock()
	defer d.mu.Unlock()

	// Buffer up events to try to avoid dropping them. This should be a
	// sufficient number for how quickly a human can manipulate buttons.
	eventC := make(chan Event, 32)

	fn := func(b []byte, delta int64) {
		// Assume 3 byte messages per the Programmer's Reference.
		if len(b) != 3 {
			return
		}

		// Cancelation takes priority over further events.
		select {
		case <-ctx.Done():
			return
		default:
		}

		// b[2] indicates whether the button was pressed or released to trigger
		// this Event.
		e := Event{On: b[2] == buttonPressed}
		switch b[0] {
		case controllerChange:
			// A button from the Automap/Live row was pressed. Map it to the
			// same X,Y system as the other buttons.
			e.X = int(b[1]) - 0x68
			e.Y = 8
		case noteOn:
			// b[1] stores both X and Y. Unpack per the reference.
			e.X = int(b[1]) % 16
			e.Y = (int(b[1]) - e.X) / 16
		default:
			// Unrecognized message, ignore in this Event API.
			return
		}

		// Either cancel or forward on the Event to the caller.
		select {
		case <-ctx.Done():
			return
		case eventC <- e:
		}
	}

	if err := d.in.SetListener(fn); err != nil {
		return nil, fmt.Errorf("failed to listen for inputs: %w", err)
	}

	// Now that the callback has been applied, we return the channel to the
	// caller and wait for ctx to be canceled. At that point, we stop listening
	// for events and close the channel to signal the consumer.
	d.wg.Add(1)
	go func() {
		defer func() {
			d.mu.Unlock()
			d.wg.Done()
		}()

		<-ctx.Done()

		// Now that the context is canceled, clean up the listener.
		d.mu.Lock()
		_ = d.in.StopListening()
		close(eventC)
	}()

	return eventC, nil
}

// Light lights the LED at coordinates (X, Y) with the specified Color. Off may
// be specified to turn off an LED. The Automap/Live row of buttons at the top
// of a Launchpad can be set by specifying y=8.
//
// To more efficiently light all LEDs with one color, use Fill instead.
func (d *Device) Light(x, y int, color Color) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.lightLocked(x, y, color)
}

// lightLocked is the internal implementation of Light. The caller must acquire
// d.mu before invoking writeLocked.
func (d *Device) lightLocked(x, y int, color Color) error {
	if y == 8 {
		// Write to top row using the appropriate command and memory offset.
		return d.writeLocked([...]byte{controllerChange, byte(0x68 + x), byte(color)})
	}

	// Write to all other rows of the LED using the standard formula.
	return d.writeLocked([...]byte{noteOn, byte(x + 0x10*y), byte(color)})
}

// Fill rapidly fills all LEDs on a Launchpad with the specified Color. Fill
// is more efficient than calling Light for each coordinate.
func (d *Device) Fill(color Color) error {
	d.mu.Lock()
	defer d.mu.Unlock()

	// 40 invocations will fill all LEDs on the device, per the reference.
	b := [...]byte{threeNoteOn, byte(color), byte(color)}
	for i := 0; i < 40; i++ {
		if err := d.writeLocked(b); err != nil {
			return err
		}
	}

	// Light the origin with the same color (a no-op) to force the device out of
	// rapid filling mode and to allow a subsequent call to Fill.
	return d.lightLocked(0, 0, color)
}

// Reset resets the Device's state by turning off all LEDs and resetting internal
// device settings.
func (d *Device) Reset() error {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.resetLocked()
}

// resetLocked is the internal implementation of Reset. The caller must acquire
// d.mu before invoking resetLocked.
func (d *Device) resetLocked() error {
	return d.writeLocked([...]byte{controllerChange, 0x00, 0x00})
}

// writeLocked writes b to a Launchpad device. The caller must acquire d.mu
// before invoking writeLocked.
func (d *Device) writeLocked(b [3]byte) error {
	// Launchpad inputs are always 3 bytes.
	n, err := d.out.Write(b[:])
	if err != nil {
		return err
	}

	if n != len(b) {
		return io.ErrShortWrite
	}

	return nil
}
