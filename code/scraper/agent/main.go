// Package agent implements runtime to scrape resource information of the node in k8s cluster, and sends
// scraped information to informer.
//
// The agent package should be used in in-cluster settings with functioning inter-node communication.
// It should be used together with informer package for proper grpc communication in cluster.
package main

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"os"
	"time"

	"example.com/m/gen"
	"github.com/shirou/gopsutil/v3/cpu"
	"github.com/shirou/gopsutil/v3/mem"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	"k8s.io/client-go/rest"
)

type (
	kubeletSummary struct {
		Pods []podStats `json:"pods"`
	}

	podStats struct {
		PodRef     podRef           `json:"podRef"`
		Containers []containerStats `json:"containers"`
	}

	podRef struct {
		Name      string `json:"name"`
		Namespace string `json:"namespace"`
	}

	containerStats struct {
		Name   string       `json:"name"`
		CPU    *cpuStats    `json:"cpu"`
		Memory *memoryStats `json:"memory"`
	}
	cpuStats struct {
		UsageNanoCores uint64 `json:"usageNanoCores"`
	}
	memoryStats struct {
		WorkingSetBytes uint64 `json:"workingSetBytes"`
	}
)

type kubeletClient struct {
	hc      *http.Client
	baseURL string
	token   string
}

func newKubeletClient(nodeIP string) (*kubeletClient, error) {
	hc := &http.Client{
		Transport: &http.Transport{
			TLSClientConfig: &tls.Config{InsecureSkipVerify: true},
		},
	}

	config, err := rest.InClusterConfig()
	if err != nil {
		return nil, err
	}

	return &kubeletClient{
		hc:      hc,
		baseURL: "https://" + nodeIP + ":10250",
		token:   config.BearerToken,
	}, nil
}

func (kc *kubeletClient) summary(ctx context.Context) (*kubeletSummary, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, kc.baseURL+"/stats/summary", nil)
	if err != nil {
		return nil, err
	}
	if kc.token != "" {
		req.Header.Set("Authorization", "Bearer "+kc.token)
	}
	resp, err := kc.hc.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("kubelete code: %d", resp.StatusCode)
	}

	var ks kubeletSummary
	if err := json.NewDecoder(resp.Body).Decode(&ks); err != nil {
		return nil, err
	}

	return &ks, nil
}

func scrape(ctx context.Context, nodeName string, kc *kubeletClient) (*gen.NodeSnapshot, error) {
	// node-level
	cpuPer, err := cpu.PercentWithContext(ctx, 0, false)
	if err != nil {
		return nil, fmt.Errorf("cpu: %v", err)
	}

	vm, err := mem.VirtualMemoryWithContext(ctx)
	if err != nil {
		return nil, fmt.Errorf("mem: %v", err)
	}

	// container-level
	summary, err := kc.summary(ctx)
	if err != nil {
		log.Printf("warn: kubelet summary: %v", err)
		summary = &kubeletSummary{}
	}

	snap := &gen.NodeSnapshot{
		NodeName:   nodeName,
		CpuPercent: cpuPer[0],
		MemUsed:    vm.Used,
		MemTotal:   vm.Total,
		Timestamp:  time.Now().UnixNano(),
	}

	for _, pod := range summary.Pods {
		for _, c := range pod.Containers {
			cm := &gen.ContainerMetrics{
				Namespace:     pod.PodRef.Namespace,
				PodName:       pod.PodRef.Name,
				ContainerName: c.Name,
			}
			if c.CPU != nil {
				cm.CpuNanoCores = c.CPU.UsageNanoCores
			}
			if c.Memory != nil {
				cm.MemWorkingSet = c.Memory.WorkingSetBytes
			}

			snap.Containers = append(snap.Containers, cm)
		}
	}
	return snap, nil
}

func stream(client gen.MetricsScraperClient, nodeName string, kc *kubeletClient) error {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	s, err := client.StreamMetrics(ctx, grpc.WaitForReady(true))
	if err != nil {
		return err
	}

	ticker := time.NewTicker(500 * time.Millisecond)
	defer ticker.Stop()

	for range ticker.C {
		snap, err := scrape(ctx, nodeName, kc)
		if err != nil {
			return err
		}
		if err := s.Send(snap); err != nil {
			return err
		}
	}
	return nil
}

func main() {
	addr := os.Getenv("INFORMER_ADDR")
	nodeName := os.Getenv("NODE_NAME")
	nodeIP := os.Getenv("NODE_IP")

	conn, err := grpc.NewClient(addr, grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		log.Fatalf("unable to create grpc connection: %v", err)
	}
	defer conn.Close()

	client := gen.NewMetricsScraperClient(conn)
	kc, err := newKubeletClient(nodeIP)
	if err != nil {
		log.Fatalf("Critical: failed getting in-cluster config: %v\n", err)
	}

	for {
		if err := stream(client, nodeName, kc); err != nil {
			log.Printf("stream error: %v -- retry in 3s", err)
			time.Sleep(3 * time.Second)
		}
	}
}
