package main

import (
	"os"
	"os/signal"
	"syscall"

	"github.com/vaskozl/minilb/internal/config"
	"github.com/vaskozl/minilb/internal/dns"
	"github.com/vaskozl/minilb/internal/k8s"
	"github.com/vaskozl/minilb/internal/routes"
)

func main() {
	config.InitFlags()

	routes.Print()

	k8s.Run()
	dns.Run()

	// Wait for termination signal
	signalChan := make(chan os.Signal, 1)
	signal.Notify(signalChan, syscall.SIGINT, syscall.SIGTERM)
	<-signalChan
}
