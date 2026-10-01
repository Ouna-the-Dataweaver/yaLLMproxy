package main

import (
	"context"
	"errors"
	"fmt"
	"log"
	"net"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"syscall"
	"time"

	"github.com/theio/yallmp-proxy/internal/config"
	"github.com/theio/yallmp-proxy/internal/forwarder"
	"github.com/theio/yallmp-proxy/internal/server"
)

func main() {
	cfg, err := config.Load("")
	if err != nil {
		log.Fatalf("load config: %v", err)
	}
	app, err := server.New(cfg)
	if err != nil {
		log.Fatalf("initialize app: %v", err)
	}

	host := cfg.ProxySettings.Server.Host
	port := cfg.ProxySettings.Server.Port
	if host == "" {
		host = cfg.GeneralSettings.Server.Host
	}
	if port == 0 {
		port = cfg.GeneralSettings.Server.Port
	}
	if envHost := os.Getenv("YALLMP_HOST"); envHost != "" {
		host = envHost
	}
	if envPort := os.Getenv("YALLMP_PORT"); envPort != "" {
		parsed, err := strconv.Atoi(envPort)
		if err != nil || parsed < 1 || parsed > 65535 {
			log.Fatalf("invalid YALLMP_PORT %q: expected an integer from 1 to 65535", envPort)
		}
		port = parsed
	}
	if host == "" {
		host = config.DefaultServerHost
	}
	if port == 0 {
		port = config.DefaultServerPort
	}

	srv := &http.Server{
		Addr:              net.JoinHostPort(host, strconv.Itoa(port)),
		Handler:           app,
		ReadHeaderTimeout: 15 * time.Second,
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	_ = app.StartHealthChecks(ctx)
	_ = app.StartModelCatalog(ctx)

	errCh := make(chan error, 3)
	go func() {
		log.Printf("yaLLMproxy listening on http://%s", srv.Addr)
		errCh <- srv.ListenAndServe()
	}()

	forwarderSettings := forwarder.SettingsFromConfig(cfg)
	if forwarderSettings.Enabled {
		_, forwarderErrCh := forwarder.StartServer(ctx, forwarderSettings)
		log.Printf("HTTP forwarder listening on http://%s -> %s", forwarderSettings.ListenAddr(), forwarderSettings.TargetBase())
		go func() {
			if err := <-forwarderErrCh; err != nil {
				errCh <- fmt.Errorf("http forwarder failed: %w", err)
			}
		}()
	}

	tcpForwarderSettings := forwarder.TCPSettingsFromConfig(cfg)
	if tcpForwarderSettings.Enabled {
		tcpServer, tcpForwarderErrCh := forwarder.StartTCPServer(ctx, tcpForwarderSettings)
		if tcpServer.Listener != nil {
			log.Printf("TCP forwarder listening on %s -> %s", tcpServer.Listener.Addr().String(), tcpForwarderSettings.TargetAddr())
		}
		go func() {
			if err := <-tcpForwarderErrCh; err != nil {
				errCh <- fmt.Errorf("tcp forwarder failed: %w", err)
			}
		}()
	}

	var runErr error
	select {
	case <-ctx.Done():
		log.Printf("shutdown requested")
	case err := <-errCh:
		if !errors.Is(err, http.ErrServerClosed) {
			runErr = fmt.Errorf("server failed: %w", err)
		}
	}
	stop()

	shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := srv.Shutdown(shutdownCtx); err != nil {
		runErr = errors.Join(runErr, fmt.Errorf("shutdown failed: %w", err))
	}
	if err := app.CloseInferenceCache(); err != nil {
		log.Printf("inference cache shutdown failed: %v", err)
	}
	if app.LogDB != nil {
		if err := app.LogDB.Close(); err != nil {
			log.Printf("log database shutdown failed: %v", err)
		}
	}
	if err := app.ShutdownObservability(shutdownCtx); err != nil {
		log.Printf("observability shutdown failed: %v", err)
	}
	if runErr != nil {
		log.Fatal(runErr)
	}
}
