package config

import (
	"flag"
	"os"
	"path/filepath"

	log "github.com/sirupsen/logrus"
	"k8s.io/client-go/util/homedir"
)

var (
	Kubeconfig = flag.String("kubeconfig", "", "Path to a kubeconfig file")
	Domain     = flag.String("domain", "minilb", "Zone under which to resolve services")
	Listen     = flag.String("listen", ":53", "Address and port to listen to")
	LogLevel   = flag.String("log-level", "info", "Log level (debug, info, warn, error, fatal, panic)")

	ResyncPeriod = flag.Int("resync", 300, "How often to check services with the API ")
	TTL          = flag.Uint("ttl", 5, "Record time to live in seconds")

	Controller = flag.Bool("controller", false, "Run the service controller in addition to the DNS server")
)

func InitFlags() {
	flag.Parse()
	// Set log level
	level, err := log.ParseLevel(*LogLevel)
	if err != nil {
		level = log.InfoLevel
	}
	log.SetLevel(level)
}

func ResolveKubeconfig() string {
	// Resolve kubeconfig path
	if *Kubeconfig == "" && !InCluster() {
		return filepath.Join(homedir.HomeDir(), ".kube", "config")
	}
	return *Kubeconfig
}

func InCluster() bool {
	// Check if running inside a cluster
	_, err := os.Stat("/var/run/secrets/kubernetes.io/serviceaccount/token")
	return err == nil
}
