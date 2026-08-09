package api

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"
)

type Node struct {
	Name       string      `json:"node"`
	CPUPercent float64     `json:"cpu_pct"`
	MemUsed    uint64      `json:"mem_used"`
	MemTotal   uint64      `json:"mem_total"`
	UpdatedAt  int64       `json:"updated_at"`
	Containers []Container `json:"containers"`
}

type ResourceSpec struct {
	CPUMillis  int64 `json:"cpu_millis"`
	MemoryByte int64 `json:"memory_bytes"`
}

type Container struct {
	Name              string  `json:"container"`
	Namespace         string  `json:"namespace"`
	PodName           string  `json:"pod"`
	CPUUsageSeconds   float64 `json:"cpu_usage_seconds"`
	CPUThrottledRatio float64 `json:"cpu_throttled_ratio"`
	MemWorkingSet     uint64  `json:"mem_working_set"`
	MemResidentSet    uint64  `json:"mem_resident_set"`
	OOM               uint64  `json:"oom"`

	CPURate        float64 `json:"cpu_rate"`
	CPUUtilization float64 `json:"cpu_utilization"`
	MemUtilization float64 `json:"mem_utilization"`

	Requests ResourceSpec `json:"requests"`
	Limits   ResourceSpec `json:"limits"`

	CPUUtilizationHistory []float64 `json:"cpu_utilization_history"`
	MemUtilizationHistory []float64 `json:"mem_utilization_history"`
}

var defaultHTTPClient = &http.Client{Timeout: 5 * time.Second}

func FetchMetrics(ctx context.Context, localPort int) ([]Node, error) {
	if localPort <= 0 {
		return nil, fmt.Errorf("invalid local port")
	}

	// TODO: make var for endpoint
	url := fmt.Sprintf("http://127.0.0.1:%d/v3/metrics", localPort)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}

	resp, err := defaultHTTPClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("metrics endpoint request failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("metrics endpoint returned %s", strings.TrimSpace(resp.Status))
	}

	var nodes []Node
	if err := json.NewDecoder(resp.Body).Decode(&nodes); err != nil {
		return nil, fmt.Errorf("invalid metrics response: %w", err)
	}

	return nodes, nil
}
