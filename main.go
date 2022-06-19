package main

import (
	"encoding/json"
	"os"
	"strconv"
	"strings"

	"github.com/Ullaakut/nmap"
	scandaloriantypes "github.com/charles-d-burton/scandalorian-types"
	"github.com/kelseyhightower/envconfig"
	"github.com/rs/zerolog"
	"github.com/rs/zerolog/log"
)

const (
	streamName   = "scan-engine"
	durableName  = "engine"
	subscription = "scan-engine.scans"
	publish      = "scan-collector.scan-results"
)

var (
	workQueue = make(chan *scandaloriantypes.PortScan, 5)
)

type ConfigSpec struct {
	LogLevel string
	BusHost  string `required:"true"`
	BusPort  string `required:"true"`
	Workers  int
}

//NMAPWorker Object to run scans
type NMAPWorker struct {
}

func main() {
	zerolog.TimeFieldFormat = zerolog.TimeFormatUnix
	errChan := make(chan error, 10)

	var cs ConfigSpec
	err := envconfig.Process("scanengine", &cs)
	log.Info().Msg(cs.BusHost)

	if cs.Workers == 0 {
		cs.Workers = 5
	}

	switch cs.LogLevel {
	case "debug":
		zerolog.SetGlobalLevel(zerolog.DebugLevel)
	case "info":
		zerolog.SetGlobalLevel(zerolog.InfoLevel)
	default:
		zerolog.SetGlobalLevel(zerolog.InfoLevel)
	}

	host := cs.BusHost
	var bus MessageBus

	if strings.Contains(host, "nats") {
		var nats NatsConn
		bus = &nats
	} else {
		log.Fatal().Err(err).Msg("Unknown protocol for message bus.  Must be one of \"nats\" ")
	}
	log.Info().Msgf("connecting to message bus: %v:%v", host, cs.BusPort)
	bus.Connect(host, cs.BusPort, errChan)
	//Initialize the worker channels by interface
	err = createWorkerPool(cs.Workers, bus)
	if err != nil {
		log.Fatal().Err(err)
	}

	go func() {
		log.Info().Msg("subscribing to bus")
		dch := bus.Subscribe(cs.Workers, errChan)
		if err != nil {
			log.Fatal().Err(err)
		}
		for message := range dch { //Wait for incoming scan requests
			log.Debug().Msg(string(message.Data))
			var scan scandaloriantypes.PortScan
			err := json.Unmarshal(message.Data, &scan)
			if err != nil {
				log.Error().Msg("unable to unmarshal data")
				log.Debug().Msg(err.Error())
				message.Nak()
				continue
			}
			message.Ack()      //Need to figure this out later, could lose scans here
			workQueue <- &scan //publish work
		}
	}()

	for err := range errChan {
		bus.Close()
		if err != nil {
			log.Error().Msg("received error from message bus")
			log.Debug().Err(err)
		}
		log.Error().Msg(err.Error())
		os.Exit(1)
	}
}

//createWorkerPool generates the worker queues and the workers to process them
func createWorkerPool(workers int, bus MessageBus) error {
	for w := 1; w <= workers; w++ {
		var worker NMAPWorker
		go worker.start(w, bus)
	}
	return nil
}

type Run struct {
	Run       *nmap.Run `json:"nmap_result"`
	IP        string    `json:"ip"`
	ScanID    string    `json:"scan_id"`
	RequestID string    `json:"request_id"`
}

func (worker *NMAPWorker) start(id int, bus MessageBus) error {

	log.Info().Msgf("Starting NMAP Worker %d %v", id, "waiting for work...")
	for scan := range workQueue {
		if len(scan.Ports) > 0 {
			log.Info().Msgf("Scanning ports for host %v with nmap", scan.IP)
			//pdef = strings.Join(scw.Scan.Request.Ports, ",")
			scanner, err := nmap.NewScanner(
				nmap.WithTargets(scan.IP),
				nmap.WithPorts(convertIntsToStrings(scan.Ports)...),
				nmap.WithServiceInfo(),
				nmap.WithOSDetection(),
				nmap.WithScripts("./scipag_vulscan/vulscan.nse"),
				nmap.WithTimingTemplate(nmap.TimingAggressive),
				// Filter out hosts that don't have any open ports
				nmap.WithFilterHost(func(h nmap.Host) bool {
					// Filter out hosts with no open ports.
					for idx := range h.Ports {
						if h.Ports[idx].Status() == "open" {
							return true
						}
					}

					return false
				}),
			)
			if err != nil {
				log.Fatal().Err(err).Msgf("unable to create nmap scanner: %v", err)
			}
			result, warns, err := scanner.Run()
			if err != nil {
				log.Fatal().Err(err).Msgf("nmap scan failed: %v", err)
			}
			if len(warns) > 0 {
				for _, warn := range warns {
					log.Info().Msgf("Warning: %v", warn)
				}
			}
			var run Run
			run.Run = result
			run.ScanID = scan.ScanID
			run.RequestID = scan.RequestID
			bus.Publish(&run)
		}
	}
	return nil
}

func convertIntsToStrings(ports []int) []string {
	vals := make([]string, len(ports))
	for i, v := range ports {
		vals[i] = strconv.Itoa(v)
	}
	return vals
}
