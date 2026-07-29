// Agent implements runtime to scrape resource information of the node in k8s cluster, and sends
// information to the aggregator, Informer.
//
// Internally, Agent uses cAdvisor to obtain container resource metrics via the Kubelet API.
// After data post-processing, it utilizes gRPC to stream scraped information at a fixed interval of 10 sec.
// Since cAdvisor in k8s updates its internal cache roughly 10-20 sec, there is no point going faster.
// The streamed information follows the pre-defined gRPC schema in proto/metrics.proto.
//
// Agent should be used together with Informer for proper data streaming,
// and it should be used within k8s cluster with proper inter-node communication.
package main

import (
	"context"
	"crypto/tls"
	"fmt"
	"log"
	"log/slog"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"

	"example.com/m/gen"
	"example.com/m/utils"
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

// TODO: to be extracted
type RingBuffer[T any] struct {
	data     []T
	capacity int
	size     int
	head     int
}

func NewRingBuffer[T any](capacity int) *RingBuffer[T] {
	if capacity <= 0 {
		panic("capacity must be greater than zero.")
	}
	return &RingBuffer[T]{
		data:     make([]T, capacity),
		capacity: capacity,
	}
}

func (rb *RingBuffer[T]) Add(dp T) {
	rb.data[rb.head] = dp
	rb.size = min(rb.size+1, rb.capacity)
	rb.head = (rb.head + 1) % rb.capacity
}

func (rb *RingBuffer[T]) GetData() []T {
	data := make([]T, rb.size)
	s := 0
	if rb.size == rb.capacity {
		s = rb.head
	}
	for i := 0; i < rb.size; i++ {
		data[i] = rb.data[(s+i)%rb.capacity]
	}
	return data
}

var profiler *utils.Profiler

const (
	ScrapeInterval = 10 * time.Second
	RingBufferCap  = 30
)

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
		slog.Error("Failed to get in-cluster config.")
		return nil, err
	}
	clientset, err := kubernetes.NewForConfig(config)
	if err != nil {
		slog.Error("Failed to create clientset from config.")
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
		slog.Error("Failed to get in-cluster config.")
		return nil, err
	}

	return &kubeletClient{
		hc:      hc,
		baseURL: "https://" + nodeIP + ":10250",
		token:   config.BearerToken,
	}, nil
}

func (kc *kubeletClient) scrape(ctx context.Context) (map[string]*dto.MetricFamily, error) {
	defer profiler.Perf()()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, kc.baseURL+"/metrics/cadvisor", nil)
	if err != nil {
		slog.Error("Failed to create Request for kubelet.")
		return nil, err
	}
	if kc.token != "" {
		req.Header.Set("Authorization", "Bearer "+kc.token)
	}
	resp, err := kc.hc.Do(req)
	if err != nil {
		slog.Error("Failed to send request to kubelet.")
		slog.Error(fmt.Sprintf("err: %v", err))
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("kubelete code: %d", resp.StatusCode)
	}

	parser := expfmt.NewTextParser(model.LegacyValidation)
	all, err := parser.TextToMetricFamilies(resp.Body)
	if err != nil {
		slog.Error("Failed to parse raw Prometheus metrics text.")
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

type cpuSample struct {
	usage       float64
	lastUpdated time.Time
}

// we might need mutex if we ever go "concurrent collecting metrics" path
type cpuHistory struct {
	prev map[string]cpuSample
}

func newCPUHistory() *cpuHistory {
	return &cpuHistory{
		prev: make(map[string]cpuSample),
	}
}

func (ch *cpuHistory) set(k string, usage float64, t time.Time) {
	ch.prev[k] = cpuSample{
		usage:       usage,
		lastUpdated: t,
	}
}

func (ch *cpuHistory) rate(k string, usage float64, t time.Time) float64 {
	prev, ok := ch.prev[k]
	if !ok {
		ch.set(k, usage, t)
		return -1
	}

	duration := t.Sub(prev.lastUpdated).Seconds()
	if duration <= 0 {
		return -1
	}

	return (usage - prev.usage) / duration * 1000
}

type utilHistory struct {
	data map[string]*RingBuffer[float64]
}

func NewUtilHistory() *utilHistory {
	return &utilHistory{data: make(map[string]*RingBuffer[float64])}
}

func (uh *utilHistory) get(key string) *RingBuffer[float64] {
	rb, ok := uh.data[key]
	if !ok {
		rb = NewRingBuffer[float64](RingBufferCap)
		uh.data[key] = rb
	}
	return rb
}

type agent struct {
	msClient    gen.MetricsScraperClient
	nodeName    string
	kc          *kubeletClient
	sc          *specCache
	ch          *cpuHistory
	cpuUtilHist *utilHistory
	memUtilHist *utilHistory
}

func (a *agent) collect(ctx context.Context) (*gen.NodeSnapshot, error) {
	defer profiler.Perf()()

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
	mfs, err := a.kc.scrape(ctx)
	if err != nil {
		slog.Warn("Fallback to empty metrics info.")
		mfs = map[string]*dto.MetricFamily{}
	}

	cpuUsage := generateMetricsMap(mfs[ContainerCPUUsage])
	cpuThrottled := generateMetricsMap(mfs[ContainerCPUThrottled])
	cpuPeriods := generateMetricsMap(mfs[ContainerCPUPeriods])
	memWSS := generateMetricsMap(mfs[ContainerMemWSS])
	memRSS := generateMetricsMap(mfs[ContainerMemRSS])
	oom := generateMetricsMap(mfs[ContainerOOM])

	snap := &gen.NodeSnapshot{
		NodeName:   a.nodeName,
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

		cs, hasSpec := a.sc.get(k)
		if hasSpec {
			requests.CpuMillis = cs.request.cpuMilli
			requests.MemoryBytes = cs.request.memoryByte
			limits.CpuMillis = cs.limit.cpuMilli
			limits.MemoryBytes = cs.limit.memoryByte
		}

		cpuRateMillis := a.ch.rate(k, usage, time.Now())

		cpuUtilization := -1.0
		memUtilization := -1.0

		cpuRB := a.cpuUtilHist.get(k)
		memRB := a.memUtilHist.get(k)

		// TODO: we could add oom risk as well
		// oom rish makes sense when limits is set
		// otherwise, use node memeory for denominator
		// probably use additional flag to let frontend knows the result is not realistic
		if cpuRateMillis >= 0 && cs.request.cpuMilli > 0 {
			cpuUtilization = cpuRateMillis / float64(cs.request.cpuMilli)
			cpuRB.Add(cpuUtilization)
		}
		if cs.request.memoryByte > 0 {
			memUtilization = memWSS[k] / float64(cs.request.memoryByte)
			memRB.Add(memUtilization)
		}

		cm := &gen.ContainerMetrics{
			Namespace:             ns,
			PodName:               pod,
			ContainerName:         c,
			CpuUsageSeconds:       usage,
			CpuThrottledRatio:     throttledRatio,
			MemWss:                uint64(memWSS[k]),
			MemRss:                uint64(memRSS[k]),
			OomEvents:             uint64(oom[k]),
			CpuRate:               cpuRateMillis,
			CpuUtilization:        cpuUtilization,
			MemUtilization:        memUtilization,
			Requests:              requests,
			Limits:                limits,
			CpuUtilizationHistory: cpuRB.GetData(),
			MemUtilizationHistory: memRB.GetData(),
		}

		snap.Containers = append(snap.Containers, cm)
	}

	return snap, nil
}

func (a *agent) stream() error {
	ctx := context.Background()

	s, err := a.msClient.StreamMetrics(ctx, grpc.WaitForReady(true))
	if err != nil {
		slog.Error("Failed to create client streaming client")
		return err
	}

	ticker := time.NewTicker(ScrapeInterval)
	defer ticker.Stop()

	for range ticker.C {
		scrapeCtx, cancel := context.WithTimeout(ctx, 5*time.Second)

		snap, err := a.collect(scrapeCtx)
		cancel()

		if err != nil {
			slog.Error("Failed to collect metrics.")
			return err
		}
		if err := s.Send(snap); err != nil {
			slog.Error("Failed when sending snap to grpc server.")
			return err
		}
	}
	return nil
}

// logger settings
func configLogger(devMode bool) *slog.Logger {
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
	isDevMode := os.Getenv("MODE") == "dev"

	profiler = utils.NewProfiler(isDevMode)
	slog.SetDefault(configLogger(isDevMode))

	addr := os.Getenv("INFORMER_ADDR")
	nodeName := os.Getenv("NODE_NAME")
	nodeIP := os.Getenv("NODE_IP")

	if nodeName == "" {
		log.Fatalln("node name not found. pls provide node name in env")
	}

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

	msClient := gen.NewMetricsScraperClient(conn)
	kc, err := newKubeletClient(nodeIP)
	if err != nil {
		log.Fatalf("Critical: failed getting in-cluster config: %v\n", err)
	}

	agent := &agent{
		msClient:    msClient,
		nodeName:    nodeName,
		kc:          kc,
		sc:          sc,
		ch:          newCPUHistory(),
		cpuUtilHist: NewUtilHistory(),
		memUtilHist: NewUtilHistory(),
	}

	for {
		if err := agent.stream(); err != nil {
			log.Printf("stream error: %v -- retry in 3s", err)
			time.Sleep(3 * time.Second)
		}
	}
}
