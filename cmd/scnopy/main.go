// Command scnopy runs the Scanopy network discovery daemon
// (scanopy-daemon) as a supervised gokrazy service.
//
// The wrapper reads the daemon API key from a file, sets the
// SCANOPY_* environment variables the daemon expects and exec()s the
// daemon binary. All daemon state (daemon ID, host ID, config.json)
// lives in a persistent directory under /perm so it survives gokrazy
// OTA updates and reboots.
package main

import (
	"flag"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"
	"syscall"
)

func mustRead(path string) []byte {
	b, err := os.ReadFile(path)
	if err != nil {
		log.Fatalf("scnopy: reading %s: %v", path, err)
	}
	return b
}

func main() {
	var (
		binary     = flag.String("binary", "/usr/local/bin/scanopy-daemon", "path to the scanopy-daemon binary")
		serverURL  = flag.String("server-url", "http://127.0.0.1:60072", "Scanopy server URL")
		apiKeyFile = flag.String("api-key-file", "/etc/scanopy/daemon-api-key", "file containing the daemon API key")
		name       = flag.String("name", "bpi-r1", "daemon name as shown in the Scanopy UI")
		interfaces = flag.String("interfaces", "", "comma-separated interfaces to scan (empty: all)")
		configDir  = flag.String("config-dir", "/perm/scanopy", "persistent config/state directory")
		logLevel   = flag.String("log-level", "info", "daemon log level")
	)
	flag.Parse()

	if _, err := os.Stat(*apiKeyFile); err != nil {
		log.Fatalf("scnopy: reading %s: %v", *apiKeyFile, err)
	}
	key := strings.TrimSpace(string(mustRead(*apiKeyFile)))
	if key == "" || strings.Contains(key, "PLACE_") {
		log.Fatalf("scnopy: %s contains no daemon API key — create a daemon in the Scanopy UI (Discover > Daemons) and store its key in this file", *apiKeyFile)
	}

	if err := os.MkdirAll(*configDir, 0755); err != nil {
		log.Fatalf("scnopy: creating config dir %s: %v", *configDir, err)
	}
	// The daemon refuses to start if its state dir is not writable.
	if err := os.Chmod(*configDir, 0755); err != nil {
		log.Fatalf("scnopy: chmod %s: %v", *configDir, err)
	}

	env := append(os.Environ(),
		"SCANOPY_SERVER_URL="+*serverURL,
		"SCANOPY_DAEMON_API_KEY="+key,
		"SCANOPY_NAME="+*name,
		"SCANOPY_CONFIG_DIR="+*configDir,
		"SCANOPY_LOG_LEVEL="+*logLevel,
	)
	if *interfaces != "" {
		env = append(env, "SCANOPY_INTERFACES="+*interfaces)
	}

	if _, err := os.Stat(*binary); err != nil {
		log.Fatalf("scnopy: daemon binary missing: %v", err)
	}

	log.Printf("scnopy: starting %s (server %s, config dir %s)", filepath.Base(*binary), *serverURL, *configDir)
	// exec replaces this process; gokrazy supervises the resulting
	// daemon and restarts it with backoff if it exits.
	if err := syscall.Exec(*binary, []string{*binary}, env); err != nil {
		log.Fatalf("scnopy: exec %s: %v", *binary, err)
	}
	fmt.Fprintln(os.Stderr, "scnopy: unreachable")
	os.Exit(111)
}
