package main

import (
	"context"
	"log"
	"net/http"
	"os"
	"time"

	"example.com/m/gen"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
)

type kubeletSummary struct{}

type kubeletClient struct {
	hc      *http.Client
	baseURL string
	token   string
}

func newKubeletClient() *kubeletClient {
	return nil
}

func (kc *kubeletClient) summary(ctx context.Context) (*kubeletSummary, error) {
	return nil, nil
}

func scrape(ctx context.Context, nodeName string, kc *kubeletClient) (*gen.NodeSnapshot, error) {
	return nil, nil
}

func stream(client gen.MetricsScraperClient, nodeName string, kc *kubeletClient) error {
	ctx := context.Background()
	s, err := client.StreamMetrics(ctx)
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

	conn, err := grpc.NewClient(addr, grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		log.Fatalf("unable to create grpc connection: %v", err)
	}
	defer conn.Close()

	client := gen.NewMetricsScraperClient(conn)
	kc := newKubeletClient()

	for {
		if err := stream(client, nodeName, kc); err != nil {
			log.Printf("stream error: %v -- retry in 3s", err)
			time.Sleep(3 * time.Second)
		}
	}
}
