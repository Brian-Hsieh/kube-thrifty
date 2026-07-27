package main

import (
	"context"
	"fmt"
	"os"
	"strings"
	"time"

	"kube-thrifty/tui/internal/kube"
	"kube-thrifty/tui/internal/tui"

	tea "github.com/charmbracelet/bubbletea"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
)

const (
	namespace    = "kube-thrifty"
	serviceName  = "kube-thrifty-metrics-service"
	remotePort   = 8080
	tickInterval = 5 * time.Second
)

func main() {
	configureKubeRuntimeErrors()

	kubeconfigPath, err := kube.ResolveKubeconfigPath()
	if err != nil {
		exitWithMessage(err.Error())
	}

	forwarder, err := kube.NewForwarder(kubeconfigPath, namespace, serviceName, remotePort)
	if err != nil {
		exitWithMessage(err.Error())
	}

	if err := forwarder.Start(); err != nil {
		exitWithMessage(err.Error())
	}
	defer forwarder.Stop()

	model := tui.NewModel(forwarder, tickInterval)
	program := tea.NewProgram(model, tea.WithAltScreen())

	if _, err := program.Run(); err != nil {
		exitWithMessage(fmt.Sprintf("unable to run TUI: %v", err))
	}
}

func exitWithMessage(msg string) {
	fmt.Fprintln(os.Stderr, "Kube-Thrifty:", msg)
	os.Exit(1)
}

func configureKubeRuntimeErrors() {
	utilruntime.ErrorHandlers = []utilruntime.ErrorHandler{
		func(_ context.Context, err error, _ string, _ ...any) {
			if err == nil || isExpectedPortForwardDisconnect(err) {
				return
			}
		},
	}
}

func isExpectedPortForwardDisconnect(err error) bool {
	msg := strings.ToLower(err.Error())
	if !strings.Contains(msg, "error copying from local connection to remote stream") {
		return false
	}

	return strings.Contains(msg, "connection reset by peer") ||
		strings.Contains(msg, "broken pipe") ||
		strings.Contains(msg, "use of closed network connection")
}
