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

package main

import (
	"context"
	"fmt"
	"log"
	"os"
	"os/signal"

	"github.com/mdlayher/launchpad"
	"gitlab.com/gomidi/rtmididrv"
	"golang.org/x/sync/errgroup"
)

func main() {
	ll := log.New(os.Stderr, "", log.LstdFlags)

	// Probe for Launchpad devices and begin the main loop if one or more
	// are detected.
	driver, err := rtmididrv.New()
	if err != nil {
		ll.Fatalf("failed to open MIDI driver: %v", err)
	}
	defer driver.Close()

	devices, err := launchpad.Devices(driver)
	if err != nil {
		ll.Fatalf("failed to fetch Launchpad devices: %v", err)
	}

	if len(devices) == 0 {
		ll.Println("no Launchpad devices detected, exiting")
	}

	// Use a context to handle cancelation on signal.
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	var eg errgroup.Group

	eg.Go(func() error {
		// Wait for signals (configurable per-platform) and then cancel the
		// context to indicate that the process should shut down.
		sigC := make(chan os.Signal, 1)
		signal.Notify(sigC, signals()...)

		s := <-sigC
		ll.Printf("received %s, shutting down", s)
		cancel()

		// Stop handling signals at this point to allow the user to forcefully
		// terminate the binary.
		signal.Stop(sigC)
		return nil
	})

	for i, d := range devices {
		// Shadow d for use in goroutine.
		d := d

		// For each device, run the main loop until ctx is canceled.
		eg.Go(func() error {
			if err := run(ctx, i, d, ll); err != nil {
				return fmt.Errorf("failed to run on %s: %v", d, err)
			}

			return d.Close()
		})
	}

	if err := eg.Wait(); err != nil {
		ll.Fatalf("failed to run: %v", err)
	}
}

// run runs the main loop for a Launchpad device.
func run(ctx context.Context, id int, d *launchpad.Device, ll *log.Logger) error {
	log.Printf("running: %02d: %s", id, d)

	// Continue reading device inputs until ctx is canceled.
	eventC, err := d.Events(ctx)
	if err != nil {
		return fmt.Errorf("failed to listen for events: %v", err)
	}

	for e := range eventC {
		log.Printf("%02d: %+v", id, e)
	}

	return nil
}
