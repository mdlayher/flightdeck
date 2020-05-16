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

package launchpad_test

import (
	"context"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"
	"github.com/mdlayher/flightdeck/internal/launchpad"
	"gitlab.com/gomidi/midi"
	"gitlab.com/gomidi/midi/testdrv"
	"golang.org/x/sync/errgroup"
)

func TestDevices(t *testing.T) {
	var (
		lpDrv    = testDriver()
		otherDrv = testdrv.New("Generic MIDI")
	)
	defer lpDrv.Close()
	defer otherDrv.Close()

	tests := []struct {
		name    string
		drv     midi.Driver
		devices []string
	}{
		{
			name: "launchpad",
			drv:  lpDrv,
			devices: []string{
				`launchpad: input: "Launchpad Test-in", output: "Launchpad Test-out"`,
			},
		},
		{
			name: "other",
			drv:  otherDrv,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			devices, err := launchpad.Devices(tt.drv)
			if err != nil {
				t.Fatalf("failed to find devices: %v", err)
			}

			// For now, it's difficult to get properly structured information
			// about MIDI devices, so just compare the String outputs for
			// ease of testing.
			var ss []string
			for _, d := range devices {
				ss = append(ss, d.String())

				// Clean up our device handles.
				if err := d.Close(); err != nil {
					t.Fatalf("failed to close %q: %v", d, err)
				}
			}

			if diff := cmp.Diff(tt.devices, ss); diff != "" {
				t.Fatalf("unexpected devices (-want +got):\n%s", diff)
			}
		})
	}
}

func TestDeviceEventsRoundTrip(t *testing.T) {
	d := testDevice(t)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	eventC, err := d.Events(ctx)
	if err != nil {
		t.Fatalf("failed to open events: %v", err)
	}

	// Light (i, i) across the grid surface to produce launchpad Events that
	// we can later verify because the test driver pipes writes into reads.
	const max = 9

	var eg errgroup.Group
	eg.Go(func() error {
		for i := 0; i < max; i++ {
			if err := d.Light(i, i, launchpad.RedHigh); err != nil {
				return fmt.Errorf("failed to light LED: %v", err)
			}
		}

		cancel()
		return nil
	})

	var got []launchpad.Event
	for e := range eventC {
		got = append(got, e)
	}

	// Build the expected sequence: (0, 0), (1, 1), etc. for comparison.
	var want []launchpad.Event
	for i := 0; i < max; i++ {
		want = append(want, launchpad.Event{
			X: i, Y: i,
		})
	}

	if diff := cmp.Diff(want, got); diff != "" {
		t.Fatalf("unexpected captured events (-want +got):\n%s", diff)
	}
}

func TestDeviceMIDICommands(t *testing.T) {
	// Copied from internal API to avoid exporting.
	const (
		noteOn           = 0x90 // Activate an LED.
		threeNoteOn      = 0x92 // Rapid fill LEDs.
		controllerChange = 0xb0 // Control messages and top row of LEDs.
	)

	// fillBytes generates an expected byte sequence which a Launchpad would
	// consume to fill the entire grid with one color. 40 iterations to fill
	// the grid is the number specified in the programmer's reference.
	fillBytes := func(color launchpad.Color) [][]byte {
		const n = 40
		out := make([][]byte, 0, n)
		for i := 0; i < n; i++ {
			out = append(out, []byte{threeNoteOn, byte(color), byte(color)})
		}

		return out
	}

	tests := []struct {
		name string
		fn   func(d *launchpad.Device)
		out  [][]byte
	}{
		{
			name: "noop",
		},
		{
			name: "reset",
			fn: func(d *launchpad.Device) {
				if err := d.Reset(); err != nil {
					panicf("failed to reset: %v", err)
				}
			},
			out: [][]byte{{controllerChange, 0x00, 0x00}},
		},
		{
			name: "light",
			fn: func(d *launchpad.Device) {
				colors := []launchpad.Color{
					launchpad.RedLow,
					launchpad.RedMedium,
					launchpad.RedHigh,
				}

				for i, c := range colors {
					if err := d.Light(i, i, c); err != nil {
						panicf("failed to light: %v", err)
					}
				}
			},
			out: [][]byte{
				{noteOn, 0x00, byte(launchpad.RedLow)},
				{noteOn, 0x11, byte(launchpad.RedMedium)},
				{noteOn, 0x22, byte(launchpad.RedHigh)},
			},
		},
		{
			name: "fill",
			fn: func(d *launchpad.Device) {
				if err := d.Fill(launchpad.GreenHigh); err != nil {
					panicf("failed to fill: %v", err)
				}
			},
			out: func() [][]byte {
				// A single Light operation is appended to reset the device to
				// its normal non-filling mode.
				out := fillBytes(launchpad.GreenHigh)
				out = append(out, []byte{noteOn, 0x00, byte(launchpad.GreenHigh)})
				return out
			}(),
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			d := testDevice(t)

			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()

			// TODO(mdlayher): it is difficult to determine if all of the
			// callbacks have or have not fired in response to writes performed
			// by tt.fn. As such, we are temporarily using a mutex around these
			// buffers and checking the number of operations performed according
			// to the expected value in len(tt.out).
			//
			// It would be nice to change this to a channel and remove the
			// lock and retry mechanism.
			var (
				mu  sync.Mutex
				out [][]byte
			)

			consume := func(b []byte, _ int64) {
				mu.Lock()
				defer mu.Unlock()
				out = append(out, b)
			}

			if err := d.Listen(ctx, consume); err != nil {
				t.Fatalf("failed to listen: %v", err)
			}

			// Manipulate the device to trigger MIDI events.
			if tt.fn != nil {
				tt.fn(d)
			}

			// Keep checking for MIDI events until we receive the expected
			// number as defined in the test table.
			var ok bool
			for i := 0; i < 10; i++ {
				mu.Lock()
				if l := len(out); l == len(tt.out) {
					ok = true
					cancel()
					mu.Unlock()
					break
				}
				mu.Unlock()

				time.Sleep(5 * time.Millisecond)
			}
			if !ok {
				t.Fatalf("did not receive %d MIDI events", len(tt.out))
			}

			// Finally we can verify the output bytes.
			if diff := cmp.Diff(tt.out, out); diff != "" {
				t.Fatalf("unexpected MIDI commands (-want +got):\n%s", diff)
			}
		})
	}
}

func testDevice(t *testing.T) *launchpad.Device {
	t.Helper()

	drv := testDriver()

	devices, err := launchpad.Devices(drv)
	if err != nil {
		t.Fatalf("failed to get devices: %v", err)
	}

	if l := len(devices); l != 1 {
		t.Fatalf("unexpected number of test devices: %d", l)
	}

	d := devices[0]

	t.Cleanup(func() {
		_ = d.Close()
		_ = drv.Close()
	})

	return d
}

func testDriver() midi.Driver { return testdrv.New("Launchpad Test") }

func panicf(format string, a ...interface{}) {
	panic(fmt.Sprintf(format, a...))
}
