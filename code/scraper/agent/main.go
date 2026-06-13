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
	"sync"
	"time"

	"example.com/m/gen"
	"github.com/shirou/gopsutil/v3/cpu"
	"github.com/shirou/gopsutil/v3/mem"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/fields"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/cache"
)

const ScrapeInterval = 500 * time.Millisecond

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

type resourceSpec struct {
	cpuMilli   int64
	memoryByte int64
}

type containerSpec struct {
	request resourceSpec
	limit   resourceSpec
}

type specCache struct {
	mu   sync.RWMutex
	data map[string]containerSpec
}

func newSpecCache() *specCache {
	return &specCache{}
}

func (s *specCache) set(pod *corev1.Pod) {
	s.mu.Lock()
	defer s.mu.Unlock()

	for _, c := range pod.Spec.Containers {
		key := pod.Namespace + "/" + pod.Name + "/" + c.Name
		s.data[key] = containerSpec{
			request: toResourceSpec(c.Resources.Requests),
			limit:   toResourceSpec(c.Resources.Limits),
		}
	}
}

func (s *specCache) delete(pod *corev1.Pod) {
	s.mu.Lock()
	defer s.mu.Unlock()

	for _, c := range pod.Spec.Containers {
		key := pod.Namespace + "/" + pod.Name + "/" + c.Name
		delete(s.data, key)
	}
}

func (s *specCache) get(namespace, podName, containerName string) (containerSpec, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	key := namespace + "/" + podName + "/" + containerName
	cs, ok := s.data[key]
	return cs, ok
}

func toResourceSpec(rl corev1.ResourceList) resourceSpec {
	spec := resourceSpec{}
	if rl == nil {
		return spec
	}
	if q, ok := rl[corev1.ResourceCPU]; ok {
		spec.cpuMilli = q.MilliValue()
	}
	if q, ok := rl[corev1.ResourceMemory]; ok {
		spec.memoryByte = q.Value()
	}

	return spec
}

func buildClientset() (*kubernetes.Clientset, error) {
	config, err := rest.InClusterConfig()
	if err != nil {
		return nil, err
	}
	clientset, err := kubernetes.NewForConfig(config)
	if err != nil {
		return nil, err
	}
	return clientset, nil
}

func startPodInformers(ctx context.Context, nodeName string, sc *specCache) error {
	clientset, err := buildClientset()
	if err != nil {
		return err
	}

	podInformer := cache.NewSharedIndexInformer(
		cache.NewListWatchFromClient(
			clientset.CoreV1().RESTClient(),
			"pod",
			corev1.NamespaceAll,
			fields.ParseSelectorOrDie("spec.nodeName="+nodeName),
		),
		&corev1.Pod{},
		30*time.Second,
		cache.Indexers{cache.NamespaceIndex: cache.MetaNamespaceIndexFunc},
	)

	podInformer.AddEventHandler(
		cache.ResourceEventHandlerFuncs{
			AddFunc: func(obj interface{}) {
				if pod, ok := obj.(*corev1.Pod); ok {
					sc.set(pod)
				}
			},
			UpdateFunc: func(_, newObj interface{}) {
				if pod, ok := newObj.(*corev1.Pod); ok {
					sc.set(pod)
				}
			},
			DeleteFunc: func(obj interface{}) {
				if pod, ok := obj.(*corev1.Pod); ok {
					sc.delete(pod)
				}
			},
		},
	)

	go podInformer.Run(ctx.Done())

	if !cache.WaitForNamedCacheSync("kube-thrifty/pod-informer", ctx.Done(), podInformer.HasSynced) {
		return fmt.Errorf("pod informer cache sync timed out")
	}
	return nil
}

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

	ticker := time.NewTicker(ScrapeInterval)
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

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	sc := newSpecCache()
	if err := startPodInformers(ctx, nodeName, sc); err != nil {
		log.Fatalf("pod informer: %v", err)
	}

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
