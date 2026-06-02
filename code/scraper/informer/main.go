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
	"net"
	"net/http"
	"sync"
	"time"

	"example.com/m/gen"
	"google.golang.org/grpc"
)

type containerSnapshot struct {
	Namespace     string `json:"namespace"`
	PodName       string `json:"pod"`
	ContainerName string `json:"container"`
	CPUNanoCores  uint64 `json:"cpu_nano_cores"`
	MemWorkingSet uint64 `json:"mem_working_set"`
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
	mu   sync.RWMutex
	data map[string]*nodeSnapshot
}

func (c *cache) set(ns *nodeSnapshot) {
	c.mu.Lock()
	c.data[ns.NodeName] = ns
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
			return stream.SendAndClose(&gen.Ack{Ok: true})
		}
		if err != nil {
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
				Namespace:     c.Namespace,
				PodName:       c.PodName,
				ContainerName: c.ContainerName,
				CPUNanoCores:  c.CpuNanoCores,
				MemWorkingSet: c.MemWorkingSet,
			})
		}

		s.cache.set(snap)
	}
}

func main() {
	c := &cache{data: make(map[string]*nodeSnapshot)}

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
