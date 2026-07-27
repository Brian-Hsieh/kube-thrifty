// Package informer implements runtime to gather resource information of all nodes in k8s cluster, and serves
// the information.
//
// The informer package should be used in in-cluster settings with functioning inter-node communication,
// and there should only be one runtime at a given time in cluster.
// It should be used together with agent package for proper grpc communication in cluster.
package main

import (
	"encoding/json"
	"io"
	"log"
	"log/slog"
	"net"
	"net/http"
	"sync"
	"time"

	"example.com/m/gen"
	"google.golang.org/grpc"
)

type resourceSpec struct {
	CPUMillis  int64 `json:"cpu_millis"`
	MemoryByte int64 `json:"memory_bytes"`
}

type containerSnapshot struct {
	Namespace         string  `json:"namespace"`
	PodName           string  `json:"pod"`
	ContainerName     string  `json:"container"`
	CPUUsageSeconds   float64 `json:"cpu_usage_seconds"`
	CPUThrottledRatio float64 `json:"cpu_throttled_ratio"`
	MemWorkingSet     uint64  `json:"mem_working_set"`
	MemResidentSet    uint64  `json:"mem_resident_set"`
	OOM               uint64  `json:"oom"`

	CPURate        float64 `json:"cpu_rate"`
	CPUUtilization float64 `json:"cpu_utilization"`
	MemUtilization float64 `json:"mem_utilization"`

	Requests resourceSpec `json:"requests"`
	Limits   resourceSpec `json:"limits"`

	CPUUtilizationHistory []float64 `json:"cpu_utilization_history"`
	MemUtilizationHistory []float64 `json:"mem_utilization_history"`
}

type nodeSnapshot struct {
	NodeName   string              `json:"node"`
	CPUPercent float64             `json:"cpu_pct"`
	MemUsed    uint64              `json:"mem_used"`
	MemTotal   uint64              `json:"mem_total"`
	UpdatedAt  int64               `json:"updated_at"`
	Containers []containerSnapshot `json:"containers"`
}

type cache struct {
	mu          sync.RWMutex
	muIdx       sync.RWMutex
	index       map[string]int
	data        []*nodeSnapshot
	lastUpdated time.Time
}

// TODO: we have to deal with the problem when a node is killed and never recovered
func (c *cache) getIdx(nodeName string) int {
	c.muIdx.Lock()
	defer c.muIdx.Unlock()

	idx, ok := c.index[nodeName]
	if !ok {
		idx = len(c.data)
		c.index[nodeName] = idx
	}
	return idx
}

func (c *cache) set(ns *nodeSnapshot) {
	c.mu.Lock()
	idx := c.getIdx(ns.NodeName)
	if len(c.data) <= idx {
		c.data = append(c.data, ns)
	} else {
		c.data[idx] = ns
	}
	c.lastUpdated = time.Now()
	c.mu.Unlock()
}

func (c *cache) all() []*nodeSnapshot {
	c.mu.RLock()
	defer c.mu.RUnlock()

	out := make([]*nodeSnapshot, 0, len(c.data))
	for _, v := range c.data {
		out = append(out, v)
	}
	return out
}

type server struct {
	gen.UnimplementedMetricsScraperServer
	cache *cache
}

func (s *server) StreamMetrics(stream gen.MetricsScraper_StreamMetricsServer) error {
	for {
		msg, err := stream.Recv()
		if err == io.EOF {
			slog.Error("Closed stream on EOF.")
			return stream.SendAndClose(&gen.Ack{Ok: true})
		}
		if err != nil {
			slog.Error("Closed stream on other error.")
			return err
		}

		snap := &nodeSnapshot{
			NodeName:   msg.NodeName,
			CPUPercent: msg.CpuPercent,
			MemUsed:    msg.MemUsed,
			MemTotal:   msg.MemTotal,
			UpdatedAt:  time.Now().UnixNano(),
		}

		for _, c := range msg.Containers {
			snap.Containers = append(snap.Containers, containerSnapshot{
				Namespace:         c.Namespace,
				PodName:           c.PodName,
				ContainerName:     c.ContainerName,
				CPUUsageSeconds:   c.CpuUsageSeconds,
				CPUThrottledRatio: c.CpuThrottledRatio,
				MemWorkingSet:     c.MemWss,
				MemResidentSet:    c.MemRss,
				OOM:               c.OomEvents,
				CPURate:           c.CpuRate,
				CPUUtilization:    c.CpuUtilization,
				MemUtilization:    c.MemUtilization,
				Requests: resourceSpec{
					CPUMillis:  c.Requests.CpuMillis,
					MemoryByte: c.Requests.MemoryBytes,
				},
				Limits: resourceSpec{
					CPUMillis:  c.Limits.CpuMillis,
					MemoryByte: c.Limits.MemoryBytes,
				},
				CPUUtilizationHistory: c.CpuUtilizationHistory,
				MemUtilizationHistory: c.MemUtilizationHistory,
			})
		}

		s.cache.set(snap)
	}
}

func main() {
	c := &cache{index: make(map[string]int)}

	lis, err := net.Listen("tcp", ":50051")
	if err != nil {
		log.Fatalf("listen: %v", err)
	}
	grpcS := grpc.NewServer()
	gen.RegisterMetricsScraperServer(grpcS, &server{cache: c})

	go func() {
		log.Println("gRPC listening on port 50051")
		if err := grpcS.Serve(lis); err != nil {
			log.Fatalf("grpc: %v", err)
		}
	}()

	http.HandleFunc("/ready", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})

	http.HandleFunc("/v3/metrics", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		err := json.NewEncoder(w).Encode(c.all())
		if err != nil {
			log.Println("JSON encoding failed")
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
	})

	log.Println("HTTP listening on 8080")
	log.Fatal(http.ListenAndServe(":8080", nil))
}
