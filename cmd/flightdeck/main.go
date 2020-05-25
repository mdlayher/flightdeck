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
	"flag"
	"fmt"
	"log"
	"net"
	"net/http"
	"os"
	"os/signal"
	"sync"
	"time"

	"github.com/mdlayher/flightdeck/internal/keylight"
	"github.com/mdlayher/launchpad"
	"github.com/mdlayher/metricslite"
	"github.com/mdlayher/schedgroup"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"
	"gitlab.com/gomidi/rtmididrv"
	"golang.org/x/sync/errgroup"
)

func main() {
	var (
		keylightAddr = flag.String("keylight.addr", "", "HTTP address of Elgato Key Light device to control")
	)
	flag.Parse()

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

	// Initialize Prometheus metrics and create a metrics node to pass through
	// the application.
	reg := prometheus.NewPedanticRegistry()
	reg.MustRegister(
		prometheus.NewBuildInfoCollector(),
		prometheus.NewGoCollector(),
		prometheus.NewProcessCollector(prometheus.ProcessCollectorOpts{}),
	)

	mm := newMetrics(metricslite.NewPrometheus(reg))

	// TODO: configurable listen address and Prometheus capabilities.
	eg.Go(func() error {
		if err := serveHTTP(ctx, ":9740", reg); err != nil {
			return fmt.Errorf("failed to serve HTTP: %v", err)
		}

		return nil
	})

	// Optionally configure Key Light support.
	var kc *keylight.Client
	if addr := *keylightAddr; addr != "" {
		kc, err = keylight.NewClient(addr, nil)
		if err != nil {
			ll.Fatalf("failed to create keylight client: %v", err)
		}
	} else {
		ll.Println("no Elgato Key Light device configured")
	}

	for i, d := range devices {
		// Shadow d for use in goroutine.
		d := d

		// For each device, run the main loop until ctx is canceled.
		eg.Go(func() error {
			if err := run(ctx, i, d, kc, ll, mm); err != nil {
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
func run(
	ctx context.Context,
	id int,
	d *launchpad.Device,
	kc *keylight.Client,
	ll *log.Logger,
	mm *metrics,
) error {
	log.Printf("running: %02d: %s", id, d)

	// Continue reading device inputs until ctx is canceled.
	eventC, err := d.Events(ctx)
	if err != nil {
		return fmt.Errorf("failed to listen for events: %v", err)
	}

	sg := schedgroup.New(ctx)

	var i int
	colors := []launchpad.Color{
		launchpad.RedHigh,
		launchpad.OrangeHigh,
		launchpad.GreenHigh,
		launchpad.YellowHigh,
	}

	for e := range eventC {
		// Capture range variable for schedgroup goroutine.
		e := e

		// Track on/off events as they occur for the launchpad with this ID.
		mm.LaunchpadEventsTotal(fmt.Sprintf("launchpad%d", id), onOff(e.On))

		ll.Printf("%02d: %+v", id, e)

		if !e.On {
			continue
		}

		// Light this tile using the next color in the slice and then dim it
		// slightly later.
		if err := d.Light(e.X, e.Y, colors[i%len(colors)]); err != nil {
			return fmt.Errorf("failed to light: %v", err)
		}

		i++

		sg.Delay(5*time.Second, func() {
			if err := d.Light(e.X, e.Y, launchpad.Off); err != nil {
				// Lazy mode, this is still basically a demo.
				panicf("failed to dim: %v", err)
			}
		})

		// TODO: refactor out.
		if kc == nil || e.X != 0 && e.Y != 0 {
			continue
		}

		lights, err := kc.Lights(ctx)
		if err != nil {
			return fmt.Errorf("failed to fetch keylight light state: %v", err)
		}

		for _, l := range lights {
			l.On = !l.On
			ll.Printf("turning keylight %s", onOff(l.On))
		}

		if err := kc.SetLights(ctx, lights); err != nil {
			return fmt.Errorf("failed to set keylight light state: %v", err)
		}
	}

	// Don't care about context cancelation error.
	_ = sg.Wait()
	return nil
}

// serveHTTP starts the flightdeck HTTP server on addr and serves until ctx
// is canceled.
func serveHTTP(ctx context.Context, addr string, reg *prometheus.Registry) error {
	l, err := net.Listen("tcp", addr)
	if err != nil {
		return err
	}

	mux := http.NewServeMux()

	// TODO: optional Prometheus endpoint.
	mux.Handle("/metrics", promhttp.HandlerFor(reg, promhttp.HandlerOpts{}))

	srv := &http.Server{
		ReadTimeout: 1 * time.Second,
		Handler:     mux,
	}

	// Listener ready, wait for cancelation via context and serve
	// the HTTP server until context is canceled, then immediately
	// close the server.
	var wg sync.WaitGroup
	wg.Add(1)
	defer wg.Wait()

	go func() {
		defer wg.Done()
		<-ctx.Done()
		_ = srv.Close()
	}()

	if err := srv.Serve(l); err != nil && err != http.ErrServerClosed {
		return err
	}

	return nil
}

// metrics contains the metrics for a flightdeck server.
type metrics struct {
	Info metricslite.Gauge

	// TODO: eventually move metrics initialization to tentative Device interface,
	// so Devices can advertise which metrics they support.
	LaunchpadEventsTotal metricslite.Counter

	mm metricslite.Interface
}

// newMetrics initializes metrics using the input metricslite.Interface.
func newMetrics(mm metricslite.Interface) *metrics {
	m := &metrics{
		Info: mm.Gauge(
			"flightdeck_build_info",
			"Metadata about this build of flightdeck.",
			"version",
		),

		LaunchpadEventsTotal: mm.Counter(
			"flightdeck_devices_launchpad_events_total",
			"The number of events received from a given Novation Launchpad device.",
			"device", "state",
		),

		mm: mm,
	}

	m.Info(1, "development")

	return m
}

func onOff(b bool) string {
	if b {
		return "on"
	}

	return "off"
}

func panicf(format string, a ...interface{}) {
	panic(fmt.Sprintf(format, a...))
}
