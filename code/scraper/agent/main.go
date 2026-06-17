// Package agent implements runtime to scrape resource information of the node in k8s cluster, and sends
// scraped information to informer.
//
// The agent package should be used in in-cluster settings with functioning inter-node communication.
// It should be used together with informer package for proper grpc communication in cluster.
package main

import (
	"context"
	"crypto/tls"
	"fmt"
	"log"
	"log/slog"
	"net/http"
	"os"
	"runtime"
	"strings"
	"sync"
	"time"

	"example.com/m/gen"
	dto "github.com/prometheus/client_model/go"
	"github.com/prometheus/common/expfmt"
	"github.com/prometheus/common/model"
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

var devMode = false

const ScrapeInterval = 500 * time.Millisecond

const (
	ContainerCPUUsage     string = "container_cpu_usage_seconds_total"
	ContainerCPUThrottled string = "container_cpu_cfs_throttled_seconds_total"
	ContainerCPUPeriods   string = "container_cpu_cfs_periods_total"
	ContainerMemWSS       string = "container_memory_working_set_bytes"
	ContainerMemRSS       string = "container_memory_rss"
	ContainerOOM          string = "container_oom_events_total"
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

func constructKey(ns, pod, c string) string {
	return ns + "/" + pod + "/" + c
}

func deconstructKey(k string) (string, string, string) {
	vs := strings.Split(k, "/")
	return vs[0], vs[1], vs[2]
}

func newSpecCache() *specCache {
	return &specCache{data: make(map[string]containerSpec)}
}

func (s *specCache) set(pod *corev1.Pod) {
	s.mu.Lock()
	defer s.mu.Unlock()

	for _, c := range pod.Spec.Containers {
		key := constructKey(pod.Namespace, pod.Name, c.Name)
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
		key := constructKey(pod.Namespace, pod.Name, c.Name)
		delete(s.data, key)
	}
}

func (s *specCache) get(key string) (containerSpec, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()

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
			"pods",
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

var wantedMetrics = map[string]bool{
	ContainerCPUUsage:     true,
	ContainerCPUThrottled: true,
	ContainerCPUPeriods:   true,
	ContainerMemWSS:       true,
	ContainerMemRSS:       true,
	ContainerOOM:          true,
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

func (kc *kubeletClient) scrape(ctx context.Context) (map[string]*dto.MetricFamily, error) {
	defer perf("scrape")()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, kc.baseURL+"/metrics/cadvisor", nil)
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

	parser := expfmt.NewTextParser(model.LegacyValidation)
	all, err := parser.TextToMetricFamilies(resp.Body)
	if err != nil {
		return nil, err
	}

	filtered := make(map[string]*dto.MetricFamily, len(wantedMetrics))
	for s, mf := range all {
		if wantedMetrics[s] {
			filtered[s] = mf
		}
	}
	return filtered, nil
}

func getLabelValue(m *dto.Metric, label string) string {
	for _, lp := range m.GetLabel() {
		if lp.GetName() == label {
			return lp.GetValue()
		}
	}
	return ""
}

func generateMetricsMap(mf *dto.MetricFamily) map[string]float64 {
	out := make(map[string]float64)
	if mf == nil {
		return out
	}

	for _, m := range mf.GetMetric() {
		c := getLabelValue(m, "container")
		if c == "" || c == "POD" {
			continue
		}
		ns := getLabelValue(m, "namespace")
		pod := getLabelValue(m, "pod")
		key := constructKey(ns, pod, c)
		switch mf.GetType() {
		case dto.MetricType_COUNTER:
			out[key] = m.GetCounter().GetValue()
		case dto.MetricType_GAUGE:
			out[key] = m.GetGauge().GetValue()
		}
	}

	return out
}

func collect(ctx context.Context, nodeName string, kc *kubeletClient, sc *specCache) (*gen.NodeSnapshot, error) {
	defer perf("collect")()

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
	mfs, err := kc.scrape(ctx)
	if err != nil {
		log.Printf("warn: kubelet summary: %v", err)
		mfs = map[string]*dto.MetricFamily{}
	}

	cpuUsage := generateMetricsMap(mfs[ContainerCPUUsage])
	cpuThrottled := generateMetricsMap(mfs[ContainerCPUThrottled])
	cpuPeriods := generateMetricsMap(mfs[ContainerCPUPeriods])
	memWSS := generateMetricsMap(mfs[ContainerMemWSS])
	memRSS := generateMetricsMap(mfs[ContainerMemRSS])
	oom := generateMetricsMap(mfs[ContainerOOM])

	snap := &gen.NodeSnapshot{
		NodeName:   nodeName,
		CpuPercent: cpuPer[0],
		MemUsed:    vm.Used,
		MemTotal:   vm.Total,
		Timestamp:  time.Now().UnixNano(),
	}

	for k, usage := range cpuUsage {
		ns, pod, c := deconstructKey(k)

		throttledRatio := 0.0
		if cpuPeriods[k] > 0 {
			throttledRatio = cpuThrottled[k] / cpuPeriods[k]
		}

		requests := &gen.ResourceSpec{}
		limits := &gen.ResourceSpec{}

		cs, hasSpec := sc.get(k)
		if hasSpec {
			requests = &gen.ResourceSpec{
				CpuMillis:   cs.request.cpuMilli,
				MemoryBytes: cs.request.memoryByte,
			}
			limits = &gen.ResourceSpec{
				CpuMillis:   cs.limit.cpuMilli,
				MemoryBytes: cs.limit.memoryByte,
			}
		}

		// TODO: calculate utilizaiton for cpu and mem
		cpuUtilization := -1.0
		memUtilization := -1.0

		cm := &gen.ContainerMetrics{
			Namespace:         ns,
			PodName:           pod,
			ContainerName:     c,
			CpuUsageSeconds:   usage,
			CpuThrottledRatio: throttledRatio,
			MemWss:            uint64(memWSS[k]),
			MemRss:            uint64(memRSS[k]),
			OomEvents:         uint64(oom[k]),
			CpuUtilization:    cpuUtilization,
			MemUtilization:    memUtilization,
			Requests:          requests,
			Limits:            limits,
		}

		snap.Containers = append(snap.Containers, cm)
	}

	return snap, nil
}

func stream(client gen.MetricsScraperClient, nodeName string, kc *kubeletClient, sc *specCache) error {
	ctx := context.Background()

	s, err := client.StreamMetrics(ctx, grpc.WaitForReady(true))
	if err != nil {
		return err
	}

	ticker := time.NewTicker(ScrapeInterval)
	defer ticker.Stop()

	for range ticker.C {
		scrapeCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
		snap, err := collect(scrapeCtx, nodeName, kc, sc)
		cancel()

		if err != nil {
			return err
		}
		if err := s.Send(snap); err != nil {
			return err
		}
	}
	return nil
}

// helper for performance tracking
func perf(name string) func() {
	if !devMode {
		return func() {}
	}

	start := time.Now()
	pc, _, _, ok := runtime.Caller(1)
	if !ok {
		return func() {
			slog.Debug("Execution time", "func", name, "time_duration(sec)", time.Since(start).Seconds())
		}
	}

	return func() {
		s := fmt.Sprintf("time duration: %vs", time.Since(start).Seconds())
		r := slog.NewRecord(time.Now(), slog.LevelDebug, s, pc)
		_ = slog.Default().Handler().Handle(context.Background(), r)
	}
}

// logger settings
func configLogger() *slog.Logger {
	removeTime := func(gs []string, a slog.Attr) slog.Attr {
		if a.Key == slog.TimeKey && len(gs) == 0 {
			return slog.Attr{}
		}
		return a
	}

	source := false
	level := slog.LevelInfo
	if devMode {
		level = slog.LevelDebug
		source = true
	}

	opts := &slog.HandlerOptions{
		AddSource:   source,
		Level:       level,
		ReplaceAttr: removeTime,
	}
	h := slog.NewTextHandler(os.Stdout, opts)
	return slog.New(h)
}

func main() {
	if os.Getenv("MODE") == "dev" {
		devMode = true
	}
	slog.SetDefault(configLogger())

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
		if err := stream(client, nodeName, kc, sc); err != nil {
			log.Printf("stream error: %v -- retry in 3s", err)
			time.Sleep(3 * time.Second)
		}
	}
}
