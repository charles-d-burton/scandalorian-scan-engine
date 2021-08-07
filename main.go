package main

import (
	"os"
	"strings"

	"github.com/Ullaakut/nmap"
	scandaloriantypes "github.com/charles-d-burton/scandalorian-types"
	jsoniter "github.com/json-iterator/go"
	log "github.com/sirupsen/logrus"
	"github.com/spf13/viper"
)

const (
	streamName   = "scan-engine"
	durableName  = "engine"
	subscription = "scan-engine.scans"
	publish      = "scan-collector.scan-results"
)

var (
	workers   int
	workQueue = make(chan *scandaloriantypes.Scan, 5)
	json      = jsoniter.ConfigCompatibleWithStandardLibrary
)

//NMAPWorker Object to run scans
type NMAPWorker struct {
}

func main() {
	errChan := make(chan error, 10)

	log.SetFormatter(&log.JSONFormatter{})
	v := viper.New()
	v.SetEnvPrefix("engine")
	v.AutomaticEnv()
	if !v.IsSet("port") || !v.IsSet("host") {
		log.Fatal("Must set host and port for message bus")
	}
	if !v.IsSet("workers") {
		workers = 5
	} else {
		workers = v.GetInt("workers")
		if workers < 1 {
			workers = 5
		}
	}

	if !v.IsSet("log_level") {
		log.SetLevel(log.InfoLevel)
	} else {
		level, err := log.ParseLevel(v.GetString("log_level"))
		if err != nil {
			log.SetLevel(log.InfoLevel)
			log.Warn(err)
		} else {
			log.Info("setting log level to debug")
			log.SetLevel(level)
		}
	}
	host := v.GetString("host")
	var bus MessageBus
	if strings.Contains(host, "nats") {
		var nats NatsConn
		bus = &nats
	} else {
		log.Error("Unknown protocol for message bus host")
	}

	bus.Connect(host, v.GetString("port"), errChan)
	//Initialize the worker channels by interface
	err := createWorkerPool(workers, bus)
	if err != nil {
		log.Fatal(err)
	}

	go func() {
		dch := bus.Subscribe(errChan)
		if err != nil {
			log.Fatal(err)
		}
		for message := range dch { //Wait for incoming scan requests
			log.Info(string(message.Data))
			var scan scandaloriantypes.Scan
			err := json.Unmarshal(message.Data, &scan)
			if err != nil {
				log.Error(err)
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
			log.Fatal(err)
		}
		log.Error("unkonown error")
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

	log.Infof("Starting NMAP Worker %d", id, "waiting for work...")
	for scan := range workQueue {
		if len(scan.Ports) > 0 {
			log.Infof("Scanning ports for host %v with nmap", scan.IP)
			//pdef = strings.Join(scw.Scan.Request.Ports, ",")
			scanner, err := nmap.NewScanner(
				nmap.WithTargets(scan.IP),
				nmap.WithPorts(scan.Ports...),
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
				log.Fatalf("unable to create nmap scanner: %v", err)
			}
			result, warns, err := scanner.Run()
			if err != nil {
				log.Fatalf("nmap scan failed: %v", err)
			}
			if len(warns) > 0 {
				for _, warn := range warns {
					log.Infof("Warning: %v", warn)
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
