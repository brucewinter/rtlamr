// RTLAMR - An rtl-sdr receiver for smart meters operating in the 900MHz ISM band.
// Copyright (C) 2014 Douglas Hall
//
// This program is free software: you can redistribute it and/or modify
// it under the terms of the GNU Affero General Public License as published
// by the Free Software Foundation, either version 3 of the License, or
// (at your option) any later version.
//
// This program is distributed in the hope that it will be useful,
// but WITHOUT ANY WARRANTY; without even the implied warranty of
// MERCHANTABILITY or FITNESS FOR A PARTICULAR PURPOSE.  See the
// GNU Affero General Public License for more details.
//
// You should have received a copy of the GNU Affero General Public License
// along with this program.  If not, see <http://www.gnu.org/licenses/>.

package main

import (
	"encoding/xml"
	"flag"
	"fmt"
	"log"
	"os"
	"os/signal"
	"runtime/pprof"
	"strings"
	"time"

	"github.com/bemasher/rtlamr/decode"
	"github.com/bemasher/rtlamr/idm"
	"github.com/bemasher/rtlamr/parse"
	"github.com/bemasher/rtlamr/scm"
	"github.com/bemasher/rtltcp"
)

const (
	CenterFreq = 920299072
)

var rcvr Receiver

type Receiver struct {
	rtltcp.SDR
	d decode.Decoder
	p parse.Parser
}

func (rcvr *Receiver) NewReceiver() {
	switch strings.ToLower(*msgType) {
	case "scm":
		rcvr.d = decode.NewDecoder(scm.NewPacketConfig(*symbolLength), *fastMag)
		rcvr.p = scm.NewParser()
	case "idm":
		rcvr.d = decode.NewDecoder(idm.NewPacketConfig(*symbolLength), *fastMag)
		rcvr.p = idm.NewParser()
	default:
		log.Fatalf("Invalid message type: %q\n", *msgType)
	}

	if !*quiet {
		rcvr.d.Cfg.Log()
		log.Println("CRC:", rcvr.p)
	}

	// Connect to rtl_tcp server.
	if err := rcvr.Connect(nil); err != nil {
		log.Fatal(err)
	}

	rcvr.HandleFlags()

	// Tell the user how many gain settings were reported by rtl_tcp.
	if !*quiet {
		log.Println("GainCount:", rcvr.SDR.Info.GainCount)
	}

	centerfreqFlagSet := false
	sampleRateFlagSet := false
	gainFlagSet := false
	flag.Visit(func(f *flag.Flag) {
		switch f.Name {
		case "centerfreq":
			centerfreqFlagSet = true
		case "samplerate":
			sampleRateFlagSet = true
		case "gainbyindex", "tunergainmode", "tunergain", "agcmode":
			gainFlagSet = true
		}
	})

	// Set some parameters for listening.
	if !centerfreqFlagSet {
		rcvr.SetCenterFreq(uint32(rcvr.Flags.CenterFreq))
	}

	if !sampleRateFlagSet {
		rcvr.SetSampleRate(uint32(rcvr.d.Cfg.SampleRate))
	}
	if !gainFlagSet {
		rcvr.SetGainMode(true)
	}

	return
}

func (rcvr *Receiver) Run() {
	// Setup signal channel for interruption.
	sigint := make(chan os.Signal, 1)
	signal.Notify(sigint, os.Kill, os.Interrupt)

	// Setup time limit channel
	tLimit := make(<-chan time.Time, 1)
	if *timeLimit != 0 {
		tLimit = time.After(*timeLimit)
	}

	block := make([]byte, rcvr.d.Cfg.BlockSize2)

	start := time.Now()
	for {
		// Exit on interrupt or time limit, otherwise receive.
		select {
		case <-sigint:
			return
		case <-tLimit:
			fmt.Println("Time Limit Reached:", time.Since(start))
			return
		default:
			// Read new sample block.
			_, err := rcvr.Read(block)
			if err != nil {
				log.Fatal("Error reading samples: ", err)
			}

			pktFound := false
			for _, pkt := range rcvr.d.Decode(block) {
				scm, err := rcvr.p.Parse(parse.NewDataFromBytes(pkt))
				if err != nil {
					// log.Println(err)
					continue
				}

				if len(meterID) > 0 && !meterID[uint(scm.MeterID())] {
					continue
				}

				if len(meterType) > 0 && !meterType[uint(scm.MeterType())] {
					continue
				}

				var msg parse.LogMessage
				msg.Time = time.Now()
				msg.Offset, _ = sampleFile.Seek(0, os.SEEK_CUR)
				msg.Length = rcvr.d.Cfg.BufferLength << 1
				msg.Message = scm

				if encoder == nil {
					// A nil encoder is just plain-text output.
					if *sampleFilename == os.DevNull {
						fmt.Fprintln(logFile, msg.StringNoOffset())
					} else {
						fmt.Fprintln(logFile, msg)
					}
				} else {
					err = encoder.Encode(msg)
					if err != nil {
						log.Fatal("Error encoding message: ", err)
					}

					// The XML encoder doesn't write new lines after each
					// element, add them.
					if _, ok := encoder.(*xml.Encoder); ok {
						fmt.Fprintln(logFile)
					}
				}

				pktFound = true
				if *single {
					break
				}
			}

			if pktFound {
				if *sampleFilename != os.DevNull {
					_, err = sampleFile.Write(rcvr.d.IQ)
					if err != nil {
						log.Fatal("Error writing raw samples to file:", err)
					}
				}
				if *single {
					return
				}
			}
		}
	}
}

func init() {
	log.SetFlags(log.Lshortfile | log.Lmicroseconds)
}

var cpuprofile = flag.String("cpuprofile", "", "write cpu profile to this file")

func main() {
	rcvr.RegisterFlags()
	RegisterFlags()

	flag.Parse()
	HandleFlags()

	rcvr.NewReceiver()

	defer logFile.Close()
	defer sampleFile.Close()
	defer rcvr.Close()

	if *cpuprofile != "" {
		f, err := os.Create(*cpuprofile)
		if err != nil {
			log.Fatal(err)
		}
		pprof.StartCPUProfile(f)
		defer pprof.StopCPUProfile()
	}

	rcvr.Run()
}
