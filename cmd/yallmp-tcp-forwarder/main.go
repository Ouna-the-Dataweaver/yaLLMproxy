package main

import (
	"context"
	"log"
	"os"
	"os/signal"
	"syscall"

	"github.com/theio/yallmp-proxy/internal/config"
	"github.com/theio/yallmp-proxy/internal/forwarder"
)

func main() {
	cfg, err := config.Load("")
	if err != nil {
		log.Fatalf("load config: %v", err)
	}
	settings := forwarder.TCPSettingsFromConfig(cfg)
	settings.Enabled = true

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	server, errCh := forwarder.StartTCPServer(ctx, settings)
	if server.Listener == nil {
		if err := <-errCh; err != nil {
			log.Fatalf("tcp forwarder failed: %v", err)
		}
		return
	}
	log.Printf("TCP forwarder listening on %s -> %s", server.Listener.Addr().String(), settings.TargetAddr())
	if err := <-errCh; err != nil {
		log.Fatalf("tcp forwarder failed: %v", err)
	}
}
