// Command scnopy runs the Scanopy network discovery daemon
// (scanopy-daemon) as a supervised gokrazy service.
//
// The wrapper reads the daemon API key from a file, sets the
// SCANOPY_* environment variables the daemon expects and exec()s the
// daemon binary. All daemon state (daemon ID, host ID, config.json)
// lives in a persistent directory under /perm so it survives gokrazy
// OTA updates and reboots.
//
// Before exec it waits until networking is up: the daemon snapshots
// its interface list at startup, and if it starts before
// gokrazy-router has created the VLAN bridges, scanopy drops every
// subnet behind them on the next discovery.
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"
)

// routerConfig is the subset of gokrazy-router.json vlansync/scnopy
// need: the vlan ids define the expected bridge names (br-vlan<id>).
type routerConfig struct {
	Vlans []struct {
		ID int `json:"id"`
	} `json:"vlans"`
}

func loadRouterConfig(path string) (*routerConfig, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	rc := &routerConfig{}
	if err := json.Unmarshal(b, rc); err != nil {
		return nil, err
	}
	return rc, nil
}

// hasDefaultRoute reports whether /proc/net/route contains a default
// route, i.e. WAN DHCP has finished.
func hasDefaultRoute() bool {
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

func mustRead(path string) []byte {
	b, err := os.ReadFile(path)
	if err != nil {
		log.Fatalf("scnopy: reading %s: %v", path, err)
	}
	return b
}

func main() {
	var (
		binary       = flag.String("binary", "/usr/local/bin/scanopy-daemon", "path to the scanopy-daemon binary")
		serverURL    = flag.String("server-url", "http://127.0.0.1:60072", "Scanopy server URL")
		apiKeyFile   = flag.String("api-key-file", "/etc/scanopy/daemon-api-key", "file containing the daemon API key")
		name         = flag.String("name", "bpi-r1", "daemon name as shown in the Scanopy UI")
		interfaces   = flag.String("interfaces", "", "comma-separated interfaces to scan (empty: all)")
		configDir    = flag.String("config-dir", "/perm/scanopy", "persistent config/state directory")
		routerConfig = flag.String("router-config", "/etc/gokrazy-router.json", "gokrazy-router config (bridges derived from vlans[].id)")
		logLevel     = flag.String("log-level", "info", "daemon log level")
		waitIfaces   = flag.Bool("wait-interfaces", true, "wait until networking is up (default route + router vlan bridges) before exec")
	)
	flag.Parse()

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

	// Wait until the WAN default route exists and every VLAN bridge
	// from the router config is present. The daemon snapshots its
	// interfaces at startup; starting too early makes scanopy drop all
	// subnets behind the not-yet-existing bridges.
	if *waitIfaces {
		deadline := time.Now().Add(5 * time.Minute)
		for {
			var missing []string
			if !hasDefaultRoute() {
				missing = append(missing, "default-route")
			}
			if rc, err := loadRouterConfig(*routerConfig); err == nil {
				for _, v := range rc.Vlans {
					b := "br-vlan" + strconv.Itoa(v.ID)
					if _, err := os.Stat(filepath.Join("/sys/class/net", b)); err != nil {
						missing = append(missing, b)
					}
				}
			}
			if len(missing) == 0 {
				break
			}
			if time.Now().After(deadline) {
				log.Printf("scnopy: network/bridges still incomplete after 5m (missing: %s) — starting anyway", strings.Join(missing, ","))
				break
			}
			log.Printf("scnopy: waiting for network (missing: %s)", strings.Join(missing, ","))
			time.Sleep(2 * time.Second)
		}
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
