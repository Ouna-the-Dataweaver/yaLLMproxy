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
	settings := forwarder.SettingsFromConfig(cfg)
	settings.Enabled = true

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	_, errCh := forwarder.StartServer(ctx, settings)
	log.Printf("HTTP forwarder listening on http://%s -> %s", settings.ListenAddr(), settings.TargetBase())
	if err := <-errCh; err != nil {
		log.Fatalf("http forwarder failed: %v", err)
	}
}
