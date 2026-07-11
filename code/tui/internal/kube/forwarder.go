package kube

import (
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/intstr"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
	"k8s.io/client-go/tools/portforward"
	"k8s.io/client-go/transport/spdy"
)

const forwardReadyTimeout = 8 * time.Second

type Forwarder struct {
	namespace   string
	serviceName string
	remotePort  int
	localPort   int
	config      *rest.Config
	clientset   *kubernetes.Clientset
	stopCh      chan struct{}
	doneCh      chan struct{}
	running     bool
	lastErr     error
	stateMu     sync.Mutex
	startStopMu sync.Mutex
}

func ResolveKubeconfigPath() (string, error) {
	if fromEnv := strings.TrimSpace(os.Getenv("KUBECONFIG")); fromEnv != "" {
		for _, candidate := range filepath.SplitList(fromEnv) {
			candidate = strings.TrimSpace(candidate)
			if candidate == "" {
				continue
			}
			if _, err := os.Stat(candidate); err == nil {
				return candidate, nil
			}
		}
		return "", fmt.Errorf("kubeconfig not found in KUBECONFIG")
	}

	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("unable to resolve home directory for kubeconfig")
	}

	defaultPath := filepath.Join(home, ".kube", "config")
	if _, err := os.Stat(defaultPath); err != nil {
		if os.IsNotExist(err) {
			return "", fmt.Errorf("kubeconfig not found at %s", defaultPath)
		}
		return "", fmt.Errorf("unable to read kubeconfig at %s", defaultPath)
	}

	return defaultPath, nil
}

func NewForwarder(kubeconfigPath, namespace, serviceName string, remotePort int) (*Forwarder, error) {
	config, err := clientcmd.BuildConfigFromFlags("", kubeconfigPath)
	if err != nil {
		return nil, fmt.Errorf("unable to load kubeconfig: %w", err)
	}

	clientset, err := kubernetes.NewForConfig(config)
	if err != nil {
		return nil, fmt.Errorf("unable to create kube client: %w", err)
	}

	if _, err := clientset.Discovery().ServerVersion(); err != nil {
		return nil, fmt.Errorf("cluster is not reachable: %w", err)
	}

	return &Forwarder{
		namespace:   namespace,
		serviceName: serviceName,
		remotePort:  remotePort,
		config:      config,
		clientset:   clientset,
	}, nil
}

func (f *Forwarder) Start() error {
	f.startStopMu.Lock()
	defer f.startStopMu.Unlock()

	if f.isRunning() {
		return nil
	}

	localPort, err := randomOpenPort()
	if err != nil {
		return fmt.Errorf("unable to allocate local port: %w", err)
	}

	stopCh := make(chan struct{})
	readyCh := make(chan struct{})
	doneCh := make(chan struct{})
	errCh := make(chan error, 1)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	podName, targetPort, err := f.resolveServiceTarget(ctx)
	if err != nil {
		return err
	}

	serverURL, err := buildPortForwardURL(f.config.Host, f.namespace, podName)
	if err != nil {
		return err
	}

	transport, upgrader, err := spdy.RoundTripperFor(f.config)
	if err != nil {
		return fmt.Errorf("unable to build port-forward transport: %w", err)
	}

	dialer := spdy.NewDialer(upgrader, &http.Client{Transport: transport}, http.MethodPost, serverURL)
	ports := []string{fmt.Sprintf("%d:%d", localPort, targetPort)}
	forwarder, err := portforward.New(dialer, ports, stopCh, readyCh, io.Discard, io.Discard)
	if err != nil {
		return fmt.Errorf("unable to initialize port-forward: %w", err)
	}

	go func() {
		defer close(doneCh)
		errCh <- forwarder.ForwardPorts()
	}()

	select {
	case <-readyCh:
		f.stateMu.Lock()
		f.stopCh = stopCh
		f.doneCh = doneCh
		f.localPort = localPort
		f.running = true
		f.lastErr = nil
		f.stateMu.Unlock()
		return nil
	case err := <-errCh:
		if err == nil {
			err = fmt.Errorf("port-forward ended before ready")
		}
		return fmt.Errorf("unable to start port-forward: %w", err)
	case <-time.After(forwardReadyTimeout):
		close(stopCh)
		return fmt.Errorf("timed out waiting for port-forward readiness")
	}
}

func (f *Forwarder) Stop() {
	f.startStopMu.Lock()
	defer f.startStopMu.Unlock()

	f.stateMu.Lock()
	stopCh := f.stopCh
	doneCh := f.doneCh
	f.stopCh = nil
	f.doneCh = nil
	f.running = false
	f.stateMu.Unlock()

	if stopCh != nil {
		close(stopCh)
	}
	if doneCh != nil {
		<-doneCh
	}
}

func (f *Forwarder) EnsureRunning() error {
	if f.isRunning() {
		return nil
	}

	if err := f.Start(); err != nil {
		f.stateMu.Lock()
		f.lastErr = err
		f.stateMu.Unlock()
		return err
	}

	return nil
}

func (f *Forwarder) LocalPort() int {
	f.stateMu.Lock()
	defer f.stateMu.Unlock()
	return f.localPort
}

func (f *Forwarder) isRunning() bool {
	f.stateMu.Lock()
	defer f.stateMu.Unlock()

	if !f.running {
		return false
	}

	select {
	case <-f.doneCh:
		f.running = false
		return false
	default:
		return true
	}
}

func (f *Forwarder) resolveServiceTarget(ctx context.Context) (string, int, error) {
	service, err := f.clientset.CoreV1().Services(f.namespace).Get(ctx, f.serviceName, metav1.GetOptions{})
	if err != nil {
		return "", 0, fmt.Errorf("unable to get service %s in namespace %s: %w", f.serviceName, f.namespace, err)
	}

	servicePort, err := findServicePort(service, f.remotePort)
	if err != nil {
		return "", 0, err
	}

	selector := labelsFromSelector(service.Spec.Selector)
	if selector == "" {
		return "", 0, fmt.Errorf("service %s has no selector", f.serviceName)
	}

	pod, err := pickRunningPod(ctx, f.clientset, f.namespace, selector)
	if err != nil {
		return "", 0, err
	}

	targetPort, err := resolveTargetPort(servicePort.TargetPort, pod)
	if err != nil {
		return "", 0, fmt.Errorf("unable to resolve target port for service %s: %w", f.serviceName, err)
	}

	return pod.Name, targetPort, nil
}

func findServicePort(service *corev1.Service, remotePort int) (corev1.ServicePort, error) {
	for _, port := range service.Spec.Ports {
		if int(port.Port) == remotePort {
			return port, nil
		}
	}

	return corev1.ServicePort{}, fmt.Errorf("service %s has no port %d", service.Name, remotePort)
}

func labelsFromSelector(selector map[string]string) string {
	if len(selector) == 0 {
		return ""
	}

	parts := make([]string, 0, len(selector))
	for key, value := range selector {
		parts = append(parts, fmt.Sprintf("%s=%s", key, value))
	}
	sort.Strings(parts)
	return strings.Join(parts, ",")
}

func pickRunningPod(ctx context.Context, clientset *kubernetes.Clientset, namespace, selector string) (*corev1.Pod, error) {
	pods, err := clientset.CoreV1().Pods(namespace).List(ctx, metav1.ListOptions{LabelSelector: selector})
	if err != nil {
		return nil, fmt.Errorf("unable to list pods for %s in namespace %s: %w", selector, namespace, err)
	}

	for i := range pods.Items {
		pod := &pods.Items[i]
		if pod.Status.Phase != corev1.PodRunning {
			continue
		}
		if podReady(pod.Status.Conditions) {
			return pod, nil
		}
	}

	for i := range pods.Items {
		pod := &pods.Items[i]
		if pod.Status.Phase == corev1.PodRunning {
			return pod, nil
		}
	}

	return nil, fmt.Errorf("no running pod found for selector %s in namespace %s", selector, namespace)
}

func podReady(conditions []corev1.PodCondition) bool {
	for _, condition := range conditions {
		if condition.Type == corev1.PodReady && condition.Status == corev1.ConditionTrue {
			return true
		}
	}
	return false
}

func resolveTargetPort(targetPort intstr.IntOrString, pod *corev1.Pod) (int, error) {
	if targetPort.Type == intstr.Int {
		return targetPort.IntValue(), nil
	}

	for _, container := range pod.Spec.Containers {
		for _, port := range container.Ports {
			if port.Name == targetPort.StrVal {
				return int(port.ContainerPort), nil
			}
		}
	}

	return 0, fmt.Errorf("named target port %q not found on pod %s", targetPort.StrVal, pod.Name)
}

func buildPortForwardURL(host, namespace, podName string) (*url.URL, error) {
	parsed, err := url.Parse(host)
	if err != nil {
		return nil, fmt.Errorf("invalid cluster host %q: %w", host, err)
	}

	if parsed.Scheme == "" || parsed.Host == "" {
		return nil, fmt.Errorf("invalid cluster host %q", host)
	}

	parsed.Path = fmt.Sprintf("/api/v1/namespaces/%s/pods/%s/portforward", namespace, podName)
	return parsed, nil
}

func randomOpenPort() (int, error) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return 0, err
	}
	defer listener.Close()

	addr, ok := listener.Addr().(*net.TCPAddr)
	if !ok {
		return 0, fmt.Errorf("unable to resolve local port")
	}

	return addr.Port, nil
}
