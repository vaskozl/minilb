package main

import (
	"os"
	"context"
	"time"

	"k8s.io/klog/v2"

	"github.com/vaskozl/minilb/internal/config"
	"github.com/vaskozl/minilb/internal/dns"
	"github.com/vaskozl/minilb/internal/k8s"
	"github.com/vaskozl/minilb/internal/routes"
)

func main() {
	config.InitFlags()
	klog.Info("Prepare to repel boarders")


	routes.Print()

	// trap Ctrl+C and call cancel on the context
	ctx := context.Background()
	ctx, cancel := context.WithCancel(ctx)

	// Enable signal handler
	signalCh := make(chan os.Signal, 2)
	defer func() {
		close(signalCh)
		cancel()
	}()

	k8s.Run(ctx)
	dns.Run()

	select {
	case <-signalCh:
		klog.Info("Exiting: received signal")
		cancel()
	case <-ctx.Done():
	}

	// grace period to cleanup resources
	time.Sleep(5 * time.Second)
}
