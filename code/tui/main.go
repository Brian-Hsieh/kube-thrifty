package main

import (
	"fmt"
	"os"
	"time"

	"kube-thrifty/tui/internal/kube"
	"kube-thrifty/tui/internal/tui"

	tea "github.com/charmbracelet/bubbletea"
)

const (
	namespace    = "kube-thrifty"
	serviceName  = "kube-thrifty-metrics-service"
	remotePort   = 8080
	tickInterval = 5 * time.Second
)

func main() {
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
