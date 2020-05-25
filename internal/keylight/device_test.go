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

package keylight_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"
	"github.com/mdlayher/flightdeck/internal/keylight"
)

func TestClientAccessoryInfo(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 250*time.Millisecond)
	defer cancel()

	want := &keylight.Device{
		ProductName:         "Elgato Key Light",
		FirmwareBuildNumber: 192,
		FirmwareVersion:     "1.0.3",
		SerialNumber:        "ABCDEFGHIJKL",
		DisplayName:         "Office",
	}

	c := testClient(t, func(w http.ResponseWriter, r *http.Request) {
		if diff := cmp.Diff("/elgato/accessory-info", r.URL.Path); diff != "" {
			panicf("unexpected URL path (-want +got):\n%s", diff)
		}

		_ = json.NewEncoder(w).Encode(want)
	})

	got, err := c.AccessoryInfo(ctx)
	if err != nil {
		t.Fatalf("failed to fetch device: %v", err)
	}

	if diff := cmp.Diff(want, got); diff != "" {
		t.Fatalf("unexpected device (-want +got):\n%s", diff)
	}
}

func TestClientErrors(t *testing.T) {
	tests := []struct {
		name  string
		fn    http.HandlerFunc
		check func(t *testing.T, err error)
	}{
		{
			name: "HTTP 404",
			fn:   http.NotFound,
			check: func(t *testing.T, err error) {
				if !strings.Contains(err.Error(), "HTTP 404") {
					t.Fatalf("error text did not contain HTTP 404: %v", err)
				}
			},
		},
		{
			name: "context canceled",
			fn: func(w http.ResponseWriter, r *http.Request) {
				// The client's context will be canceled while this sleep is
				// occurring.
				time.Sleep(500 * time.Millisecond)

				if err := r.Context().Err(); !errors.Is(err, context.Canceled) {
					panicf("expected context canceled, but got: %v", err)
				}
			},
			check: func(t *testing.T, err error) {
				if !errors.Is(err, context.DeadlineExceeded) {
					t.Fatalf("expected context deadline exceeded, but got: %v", err)
				}
			},
		},
		{
			name: "malformed JSON",
			fn: func(w http.ResponseWriter, r *http.Request) {
				_, _ = w.Write([]byte{0xff})
			},
			check: func(t *testing.T, err error) {
				var jerr *json.SyntaxError
				if !errors.As(err, &jerr) {
					t.Fatalf("expected JSON syntax error, but got: %v", err)
				}
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 250*time.Millisecond)
			defer cancel()

			c := testClient(t, tt.fn)

			// We are testing against the exported API for conciseness but
			// ultimately this test only really cares about various error
			// conditions, so we will ignore the Device return value entirely.
			_, err := c.AccessoryInfo(ctx)
			if err == nil && tt.check != nil {
				t.Fatal("an error was expected, but none occurred")
			}

			tt.check(t, err)
		})
	}
}

// testClient creates a *keylight.Client pointed at the HTTP server running with
// handler fn.
func testClient(t *testing.T, fn http.HandlerFunc) *keylight.Client {
	t.Helper()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json; charset=utf-8")

		if fn != nil {
			fn(w, r)
		}
	}))

	t.Cleanup(func() {
		srv.Close()
	})

	c, err := keylight.NewClient(srv.URL, nil)
	if err != nil {
		t.Fatalf("failed to create keylight client: %v", err)
	}

	return c

}

func panicf(format string, a ...interface{}) {
	panic(fmt.Sprintf(format, a...))
}
