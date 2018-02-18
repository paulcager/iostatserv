package main

import (
	"bufio"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"sync/atomic"
	"time"
)

type ioStats struct {
	Timestamp          time.Time
	ReadsPerSec        int64
	BytesReadPerSec    int64
	ReadMillis         int64
	WritesPerSec       int64
	BytesWrittenPerSec int64
	WriteMillis        int64
	InFlight           int64
	WaitMillis         int64
}

var (
	lastSamples    = make(map[string]*atomic.Value)
	sampleInterval time.Duration
	httpPort       string
	devices        string
)

func main() {
	flag.DurationVar(&sampleInterval, "sampleInterval", time.Second, "Sample interval")
	flag.StringVar(&httpPort, "httpPort", ":8080", "HTTP Port to listen on")
	flag.StringVar(&devices, "devices", "sda", "Comma-separated device names to report on")
	flag.Parse()

	httpPort = strings.TrimSpace(httpPort)
	devices = strings.TrimSpace(devices)
	if sampleInterval <= 0 {
		sampleInterval = time.Second
	}
	if !strings.HasPrefix(httpPort, ":") {
		httpPort = ":" + httpPort
	}
	if len(devices) == 0 {
		devices = "sda"
	}

	for _, dev := range strings.Split(devices, ",") {
		value := new(atomic.Value)
		lastSamples[dev] = value
		go monitor("/sys/block/"+dev+"/stat", value)
	}

	http.HandleFunc("/", statsHandlerAsJSON)
	http.ListenAndServe(httpPort, http.DefaultServeMux)
}

func monitor(statFile string, lastSample *atomic.Value) {
	f, err := os.Open(statFile)
	if err != nil {
		panic(err)
	}
	defer f.Close()

	r := bufio.NewReader(f)
	prev := readStat(r)
	ticker := time.NewTicker(sampleInterval)
	defer ticker.Stop()

	for {
		<-ticker.C
		_, err := f.Seek(0, 0)
		if err != nil {
			panic(err)
		}
		r.Reset(f)

		// https://www.kernel.org/doc/Documentation/block/stat.txt defines format:
		//	   Name            units         description
		//	   ----            -----         -----------
		//	 0 read I/Os       requests      number of read I/Os processed
		//	 1 read merges     requests      number of read I/Os merged with in-queue I/O
		//	 2 read sectors    sectors       number of sectors read
		//	 3 read ticks      milliseconds  total wait time for read requests
		//	 4 write I/Os      requests      number of write I/Os processed
		//	 5 write merges    requests      number of write I/Os merged with in-queue I/O
		//	 6 write sectors   sectors       number of sectors written
		//	 7 write ticks     milliseconds  total wait time for write requests
		//	 8 in_flight       requests      number of I/Os currently in flight
		//	 9 io_ticks        milliseconds  total time this block device has been active
		//	10 time_in_queue   milliseconds  total wait time for all requests

		cur := readStat(r)

		for i := range cur {
			fmt.Print(cur[i]-prev[i], "\t")
		}
		fmt.Println()
		stats := ioStats{
			Timestamp:          time.Now(),
			ReadsPerSec:        (cur[0] - prev[0]) * int64(time.Second) / int64(sampleInterval),
			BytesReadPerSec:    (cur[2] - prev[2]) * 512 * int64(time.Second) / int64(sampleInterval),
			ReadMillis:         (cur[3] - prev[3]) * int64(time.Second) / int64(sampleInterval),
			WritesPerSec:       (cur[4] - prev[4]) * int64(time.Second) / int64(sampleInterval),
			BytesWrittenPerSec: (cur[6] - prev[6]) * 512 * int64(time.Second) / int64(sampleInterval),
			WriteMillis:        (cur[7] - prev[7]) * int64(time.Second) / int64(sampleInterval),
			InFlight:           cur[8],
			WaitMillis:         (cur[10] - prev[10]) * int64(time.Second) / int64(sampleInterval),
		}
		lastSample.Store(stats)
		prev = cur

	}
}

func readStat(r io.Reader) [11]int64 {
	var cur [11]int64
	for i := range cur {
		if _, err := fmt.Fscanf(r, "%d", &cur[i]); err != nil {
			// There's a bug in either the kernel or my code. No prizes for guessing which.
			panic(err)
		}
	}
	return cur
}

func statsHandlerAsJSON(w http.ResponseWriter, r *http.Request) {
	m := make(map[string]ioStats)
	for name, value := range lastSamples {
		s := value.Load()
		if s != nil {
			m[name] = s.(ioStats)
		}
	}

	b, _ := json.Marshal(m)
	w.Header().Add("Content-Type", "application/json")
	w.Write(b)
}
