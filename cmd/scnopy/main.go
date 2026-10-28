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
	"time"
)

func mustRead(path string) []byte {
	b, err := os.ReadFile(path)
	if err != nil {
		log.Fatalf("scnopy: reading %s: %v", path, err)
	}
	return b
}

// networkReady reports whether the routing table contains a default
// route, i.e. WAN DHCP has finished and gokrazy-router has wired up
// its uplink.
func networkReady() bool {
	data, err := os.ReadFile("/proc/net/route")
	if err != nil {
		return false
	}
	for _, line := range strings.Split(string(data), "\n")[1:] {
		f := strings.Fields(line)
		if len(f) >= 8 && f[1] == "00000000" && f[7] == "00000000" {
			return true
		}
	}
	return false
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
		waitIfaces = flag.Bool("wait-interfaces", true, "wait until networking is up (default route present) before exec")
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

	// The daemon resolves its server connection at startup. On gokrazy
	// the WAN address arrives via DHCP a few seconds after boot and the
	// VLAN bridges appear when gokrazy-router has configured them.
	// Without this wait every startup announcement fails and the daemon
	// permanently enters its NOT ready state.
	if *waitIfaces {
		deadline := time.Now().Add(5 * time.Minute)
		for {
			if networkReady() {
				break
			}
			if time.Now().After(deadline) {
				log.Printf("scnopy: no usable network after 5m — starting anyway")
				break
			}
			log.Printf("scnopy: waiting for network (default route + global IPv4)")
			time.Sleep(2 * time.Second)
		}
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
