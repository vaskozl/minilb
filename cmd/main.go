package main

import (
	"context"
	"flag"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/vaskozl/minilb/internal/controller"
	"github.com/vaskozl/minilb/internal/dns"
)

var (
	kubeconfig   = flag.String("kubeconfig", "", "Path to a kubeconfig file")
	domain       = flag.String("domain", "minilb", "Zone under which to resolve services")
	listen       = flag.String("listen", ":53", "Address and port to listen on")
	resyncPeriod = flag.Int("resync", 300, "How often to check services with the API")
	ttl          = flag.Uint("ttl", 5, "Record time to live in seconds")
	upstream     = flag.String("upstream", "", "Upstream DNS server for forwarding (e.g. 1.1.1.1:53)")
	healthAddr   = flag.String("health", ":8080", "Address for health/readiness endpoint")
)

func main() {
	flag.Parse()

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	ctrl, err := controller.New(ctx, *kubeconfig, *domain, *resyncPeriod)
	if err != nil {
		slog.Error("Failed to start controller", "err", err)
		os.Exit(1)
	}
	ctrl.PrintRoutes()

	srv := dns.Run(&dns.Handler{
		Domain:   *domain,
		TTL:      uint32(*ttl),
		Resolver: ctrl,
		Upstream: *upstream,
	}, *listen)

	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) {
		if ctrl.Ready() {
			w.WriteHeader(http.StatusOK)
			w.Write([]byte("ok"))
		} else {
			w.WriteHeader(http.StatusServiceUnavailable)
			w.Write([]byte("not ready"))
		}
	})
	healthSrv := &http.Server{Addr: *healthAddr, Handler: mux}
	go func() {
		slog.Info("Health endpoint started", "addr", *healthAddr)
		if err := healthSrv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			slog.Error("Health server failed", "err", err)
		}
	}()

	<-ctx.Done()
	slog.Info("Shutting down")

	shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	srv.Shutdown(shutdownCtx)
	healthSrv.Shutdown(shutdownCtx)
}
