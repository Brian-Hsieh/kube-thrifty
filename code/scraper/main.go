package main

import (
	"context"
	"crypto/tls"
	"fmt"
	"log"
	"net/http"
	"sync"
	"time"

	"github.com/gin-gonic/gin"
	dto "github.com/prometheus/client_model/go"
	"github.com/prometheus/common/expfmt"
	"github.com/prometheus/common/model"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
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

type CPUHistory struct {
	TotalSeconds float64
	Timestamp    time.Time
}

var (
	cpuHistory = make(map[string]CPUHistory)
	historyMu  sync.Mutex
)

func main() {
	config, err := rest.InClusterConfig()
	if err != nil {
		log.Fatalf("Critical: failed getting in-cluster config: %v\n", err)
	}

	clientset, err := kubernetes.NewForConfig(config)
	if err != nil {
		log.Fatalf("Critical: failed to create k8s clientset: %v\n", err)
	}

	r := gin.Default()
	r.GET("/v2/metrics", func(ctx *gin.Context) {
		results, err := scrapeMetricsAllNodes(clientset, config.BearerToken)
		if err != nil {
			log.Printf("Scrape failed: %v\n", err)
			ctx.JSON(http.StatusInternalServerError, gin.H{"error": "Could not retrieve node metrics"})
			return
		}
		ctx.JSON(http.StatusOK, results)
	})

	r.Run(":8080")
}

func scrapeMetricsAllNodes(clientset *kubernetes.Clientset, token string) ([]Node, error) {
	var nodesMetrics []Node

	ctx := context.TODO()

	// get all nodes
	nodes, err := clientset.CoreV1().Nodes().List(ctx, metav1.ListOptions{})
	if err != nil {
		return nil, fmt.Errorf("failed to get/list nodes via k8s API: %w", err)
	}

	// get all pods for pod memory limit scraping
	pods, err := clientset.CoreV1().Pods("").List(ctx, metav1.ListOptions{})
	if err != nil {
		return nil, fmt.Errorf("failed to get/list pods via k8s API: %w", err)
	}

	// scrape container memory and cpu limit
	limitMemoryMap := make(map[string]int64)
	limitCPUMap := make(map[string]float64)

	for _, pod := range pods.Items {
		for _, container := range pod.Spec.Containers {
			k := fmt.Sprintf("%s%s%s", pod.Namespace, pod.Name, container.Name)
			limitMemoryMap[k] = container.Resources.Limits.Memory().Value()
			limitCPUMap[k] = float64(container.Resources.Limits.Cpu().MilliValue())
		}
	}

	// skip tls verification for local dev
	tr := &http.Transport{
		TLSClientConfig: &tls.Config{InsecureSkipVerify: true},
	}
	httpClient := &http.Client{Transport: tr}

	for _, node := range nodes.Items {
		nodeIP := ""
		for _, addr := range node.Status.Addresses {
			if addr.Type == "InternalIP" {
				nodeIP = addr.Address
			}
		}

		if nodeIP == "" {
			return nil, fmt.Errorf("failed to locate internal ip for node: %w", err)
		}

		nodeMetrics := Node{
			Name:              node.Name,
			InternalIP:        nodeIP,
			AllocatedMemoryMB: node.Status.Allocatable.Memory().AsApproximateFloat64() / 1024 / 1024,
			AllocatedCPUCores: float64(node.Status.Allocatable.Cpu().MilliValue()) / 1000,
		}
		var containerMemoryUsage []ContainerMemoryInfo
		var containerCPUInfo []ContainerCPUInfo

		url := fmt.Sprintf("https://%s:10250/metrics/resource", nodeIP)
		req, _ := http.NewRequest("GET", url, nil)
		req.Header.Set("Authorization", "Bearer "+token)

		resp, err := httpClient.Do(req)
		if err != nil {
			return nil, fmt.Errorf("failed when sending metrics req: %w", err)
		}
		defer resp.Body.Close()

		if resp.StatusCode != http.StatusOK {
			return nil, fmt.Errorf("unexpected status code: %d", resp.StatusCode)
		}

		// get current timestamp for cpu usage calculation
		now := time.Now()

		// parse Prometheus text format
		parser := expfmt.NewTextParser(model.LegacyValidation)
		metricFamilies, err := parser.TextToMetricFamilies(resp.Body)
		if err != nil {
			return nil, fmt.Errorf("failed to parse metrics: %w", err)
		}

		// extract container memory info
		if mf, ok := metricFamilies["container_memory_working_set_bytes"]; ok {
			for _, m := range mf.GetMetric() {
				container := getLabelValue(m.Label, "container")
				pod := getLabelValue(m.Label, "pod")
				namespace := getLabelValue(m.Label, "namespace")
				memBytes := m.GetGauge().GetValue()
				key := fmt.Sprintf("%s%s%s", namespace, pod, container)

				if pod == "" || container == "" {
					continue
				}

				containerMemoryUsage = append(containerMemoryUsage, ContainerMemoryInfo{
					Namespace: namespace,
					Pod:       pod,
					Container: container,
					UsageMB:   memBytes / 1024 / 1024,
					LimitMB:   float64(limitMemoryMap[key]) / 1024 / 1024,
				})
			}

			nodeMetrics.ContainerMemoryInfo = containerMemoryUsage
		} else {
			return nil, fmt.Errorf("metric 'container_memory_working_set_bytes' not found in stream.", nil)
		}

		// extract container cpu info
		if mf, ok := metricFamilies["container_cpu_usage_seconds_total"]; ok {
			for _, m := range mf.GetMetric() {
				container := getLabelValue(m.Label, "container")
				pod := getLabelValue(m.Label, "pod")
				namespace := getLabelValue(m.Label, "namespace")
				curValue := m.GetCounter().GetValue()
				key := fmt.Sprintf("%s%s%s", namespace, pod, container)

				if pod == "" || container == "" {
					continue
				}

				historyMu.Lock()
				prev, found := cpuHistory[key]
				cpuHistory[key] = CPUHistory{TotalSeconds: curValue, Timestamp: now}
				historyMu.Unlock()

				var cpuCores float64
				if found {
					diff := now.Sub(prev.Timestamp).Seconds()
					if diff > 0 && curValue >= prev.TotalSeconds {
						cpuCores = (curValue - prev.TotalSeconds) / diff
					} else {
						cpuCores = 0
					}
				}

				containerCPUInfo = append(containerCPUInfo, ContainerCPUInfo{
					Namespace:  namespace,
					Pod:        pod,
					Container:  container,
					UsageCores: cpuCores,
					LimitCores: limitCPUMap[key] / 1000,
				})
			}
			nodeMetrics.ContainerCPUInfo = containerCPUInfo
		}

		nodesMetrics = append(nodesMetrics, nodeMetrics)
	}
	return nodesMetrics, nil
}

// helper to extract label values from the Prometheus Metric
func getLabelValue(labels []*dto.LabelPair, target string) string {
	for _, l := range labels {
		if l.GetName() == target {
			return l.GetValue()
		}
	}
	return ""
}
