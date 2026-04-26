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
	Name                string                `json:"name"`
	InternalIP          string                `json:"internal_ip"`
	ContainerMemoryInfo []ContainerMemoryInfo `json:"container_memory_info"`
	ContainerCPUInfo    []ContainerCPUInfo    `json:"container_cpu_info"`
	AllocatedMemoryMB   float64               `json:"allocated_memory_mb"`
	AllocatedCPUCores   float64               `json:"allocated_cpu_cores"`
}

type ContainerCPUInfo struct {
	Namespace  string  `json:"namespace"`
	Pod        string  `json:"pod"`
	Container  string  `json:"container"`
	UsageCores float64 `json:"usage_cores"`
	LimitCores float64 `json:"limit_cores"`
}

type ContainerMemoryInfo struct {
	Namespace string  `json:"namespace"`
	Pod       string  `json:"pod"`
	Container string  `json:"container"`
	UsageMB   float64 `json:"usage_mb"`
	LimitMB   float64 `json:"limit_mb"`
}

var defaultHTTPClient = &http.Client{Timeout: 5 * time.Second}

func FetchMemory(ctx context.Context, localPort int) ([]Node, error) {
	if localPort <= 0 {
		return nil, fmt.Errorf("invalid local port")
	}

	// TODO: make var for endpoint
	url := fmt.Sprintf("http://127.0.0.1:%d/v2/metrics", localPort)
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
