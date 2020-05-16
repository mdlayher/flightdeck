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
	"testing"

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
