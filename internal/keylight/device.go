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

package keylight

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"time"
)

// A Client can control Elgato Key Light devices.
type Client struct {
	c *http.Client
	u *url.URL
}

// NewClient creates a Client for the Key Light specified by addr. If c is nil,
// a default HTTP client will be configured.
func NewClient(addr string, c *http.Client) (*Client, error) {
	if c == nil {
		c = &http.Client{Timeout: 2 * time.Second}
	}

	u, err := url.Parse(addr)
	if err != nil {
		return nil, err
	}

	return &Client{
		c: c,
		u: u,
	}, nil
}

// A Device contains metadata about an Elgato Key Light device.
type Device struct {
	ProductName         string `json:"productName"`
	FirmwareBuildNumber int    `json:"firmwareBuildNumber"`
	FirmwareVersion     string `json:"firmwareVersion"`
	SerialNumber        string `json:"serialNumber"`
	DisplayName         string `json:"displayName"`

	// TODO: add hardwareBoardType, features?
}

// AccessoryInfo fetches information about a Key Light device.
func (c *Client) AccessoryInfo(ctx context.Context) (*Device, error) {
	var d Device
	if err := c.do(ctx, http.MethodGet, "/elgato/accessory-info", nil, &d); err != nil {
		return nil, err
	}

	return &d, nil
}

// do performs an HTTP request with the input parameters, optionally
// unmarshaling a JSON body into out if out is not nil.
func (c *Client) do(ctx context.Context, method, path string, body io.Reader, out interface{}) error {
	// Make a copy of c.u before manipulating the path to avoid modifying the
	// base URL.
	u := *c.u
	u.Path = path

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), body)
	if err != nil {
		return err
	}

	res, err := c.c.Do(req)
	if err != nil {
		return err
	}
	defer res.Body.Close()

	if res.StatusCode != http.StatusOK {
		return fmt.Errorf("keylight: device returned HTTP %d", res.StatusCode)
	}

	if out == nil {
		// No struct passed to unmarshal from JSON, exit early.
		return nil
	}

	if err := json.NewDecoder(res.Body).Decode(out); err != nil {
		return err
	}

	return nil
}
