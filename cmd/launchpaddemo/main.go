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

// Command launchpaddemo provides an interactive demo of the launchpad package
// using a connected Novation Launchpad device.
package main

import (
	"context"
	"fmt"
	"log"
	"math/rand"
	"os"
	"os/signal"
	"sync"
	"time"

	"github.com/mdlayher/flightdeck/internal/launchpad"
	"gitlab.com/gomidi/rtmididrv"
	"golang.org/x/sync/errgroup"
)

func main() {
	// Use a context to handle cancelation on signal.
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	var wg sync.WaitGroup
	wg.Add(1)
	defer wg.Wait()

	go func() {
		defer wg.Done()

		// Wait for signals to cancel the context and indicate that the process
		// should shut down.
		sigC := make(chan os.Signal, 1)
		signal.Notify(sigC, os.Interrupt)

		s := <-sigC
		log.Printf("received %s, shutting down", s)
		cancel()

		// Stop handling signals at this point to allow the user to forcefully
		// terminate the binary.
		signal.Stop(sigC)
	}()

	// Open the driver and use it to detect Launchpad devices.
	drv, err := rtmididrv.New()
	if err != nil {
		log.Fatalf("failed to open driver: %v", err)
	}
	defer drv.Close()

	devices, err := launchpad.Devices(drv)
	if err != nil {
		log.Fatalf("failed to find launchpads: %v", err)
	}

	// For each detected device, run the main loop until canceled.
	var eg errgroup.Group
	for i, d := range devices {
		// Shadow to pass d to closure goroutine.
		d := d
		log.Printf("%02d: %s", i, d)

		eg.Go(func() error {
			if err := run(ctx, d); err != nil {
				return fmt.Errorf("failed to run %q: %v", d, err)
			}

			return nil
		})
	}

	if err := eg.Wait(); err != nil {
		log.Fatalf("failed to wait: %v", err)
	}
}

// run begins running the demo on a Launchpad.
func run(ctx context.Context, d *launchpad.Device) error {
	eventC, err := d.Events(ctx)
	if err != nil {
		return fmt.Errorf("failed to listen for events: %v", err)
	}

	if err := write(ctx, d, eventC); err != nil && err != context.Canceled {
		return fmt.Errorf("failed to write to device: %v", err)
	}

	if err := d.Close(); err != nil {
		return fmt.Errorf("failed to close device: %v", err)
	}

	return nil
}

// write writes data to a Launchpad device by filling the LED grid and
// accepting user input events.
func write(ctx context.Context, d *launchpad.Device, eventC <-chan launchpad.Event) error {
	r := rand.New(rand.NewSource(time.Now().UnixNano()))

	var eg errgroup.Group
	eg.Go(func() error {
		for {
			// Read events until context canceled or channel closed.
			var (
				e  launchpad.Event
				ok bool
			)

			select {
			case e, ok = <-eventC:
				if !ok {
					return ctx.Err()
				}
			case <-ctx.Done():
				return ctx.Err()
			}

			// Got an event, log it and light or dim the given LED depending
			// on whether it is held or not.
			var (
				state = "off"
				color = launchpad.Off
			)

			if e.On {
				state = "on"
				color = launchpad.RedHigh
			}

			log.Printf("input: (%d, %d): %s", e.X, e.Y, state)

			if err := d.Light(e.X, e.Y, color); err != nil {
				return fmt.Errorf("failed to light LED: %v", err)
			}

			// Clear grid LEDs at random on each event, but never clear the
			// LED that triggered this event.
			for i := 0; i < 16; i++ {
				x, y := r.Intn(8), r.Intn(8)
				if x == e.X && y == e.Y {
					continue
				}

				if err := d.Light(x, y, 0); err != nil {
					return fmt.Errorf("failed to clear LED: %v", err)
				}
			}
		}
	})

	eg.Go(func() error {
		// Continuously fill the launchpad LED grid with different colors at a
		// regular interval until context is canceled.
		for {
			for _, c := range []launchpad.Color{
				launchpad.GreenLow, launchpad.GreenMedium, launchpad.GreenHigh,
				launchpad.OrangeLow, launchpad.OrangeMedium, launchpad.OrangeHigh,
				launchpad.YellowLow, launchpad.YellowMedium, launchpad.YellowHigh,
				launchpad.RedLow, launchpad.RedMedium, launchpad.RedHigh,
			} {
				if err := d.Fill(c); err != nil {
					return fmt.Errorf("failed to fill LEDs: %v", err)
				}

				select {
				case <-ctx.Done():
					return nil
				case <-time.After(2 * time.Second):
				}
			}
		}
	})

	return eg.Wait()
}
