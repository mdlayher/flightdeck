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
	"log"

	"github.com/mdlayher/launchpad"
	"gitlab.com/gomidi/rtmididrv"
)

func main() {
	driver, err := rtmididrv.New()
	if err != nil {
		log.Fatalf("failed to open MIDI driver: %v", err)
	}

	devices, err := launchpad.Devices(driver)
	if err != nil {
		log.Fatalf("failed to fetch Launchpad devices: %v", err)
	}

	if len(devices) == 0 {
		log.Println("no Launchpad devices detected, exiting")
	}

	for i, d := range devices {
		log.Printf("%02d: %s", i, d)
	}
}
