// Command vlansync keeps Scanopy's VLAN model in sync with the
// VLANs configured on this gokrazy router (gokrazy-router).
//
// It reads /etc/gokrazy-router.json (written by the gokrazy packer from
// the instance's router.json), waits until the VLAN bridges exist and
// the network is up, then creates/updates the matching VLANs in
// Scanopy — including the subnet and interface (physical port)
// associations. Run it as a second gokrazy service next to scnopy; it
// re-checks on every interval.
//
// Authentication uses a Scanopy USER api key (daemon keys may not
// touch VLANs), read from /etc/scanopy/user-api-key.
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

type routerConfig struct {
	Vlans []struct {
		ID      int      `json:"id"`
		Name    string   `json:"name"`
		Address string   `json:"address"`
		Ports   []string `json:"ports"`
	} `json:"vlans"`
	Wifi struct {
		Enabled bool   `json:"enabled"`
		Address string `json:"address"`
	} `json:"wifi"`
	Services struct {
		Mount struct {
			Device string `json:"device"`
		} `json:"mount"`
	} `json:"services"`
}

type vlanTarget struct {
	Number int
	Name   string
	CIDRs  []string // normalized networks, e.g. 10.0.1.0/24
	Ports  []string // interface names, e.g. lan1
}

func main() {
	var (
		serverURL  = flag.String("server-url", "http://127.0.0.1:60072", "Scanopy server URL")
		apiKeyFile = flag.String("api-key-file", "/etc/scanopy/user-api-key", "file containing a Scanopy USER api key")
		routerConf = flag.String("router-config", "/etc/gokrazy-router.json", "gokrazy-router config (staged from router.json)")
		wifiVlan   = flag.Int("wifi-vlan", 0, "vlan number to also associate the wifi subnet with (0: none)")
		interval   = flag.Duration("interval", 30*time.Minute, "re-sync interval")
		logLevel   = flag.String("log-level", "info", "log level")
	)
	flag.Parse()

	token := strings.TrimSpace(string(mustRead(*apiKeyFile)))
	if token == "" || strings.Contains(token, "PLACE_") {
		log.Fatalf("vlansync: %s contains no user api key — create one in the Scanopy UI (Settings > API keys) and store it in this file", *apiKeyFile)
	}

	waitForNetwork()

	for {
		if err := syncOnce(*serverURL, token, *routerConf, *wifiVlan, *logLevel); err != nil {
			log.Printf("vlansync: sync failed: %v (retrying in 2m)", err)
			time.Sleep(2 * time.Minute)
			continue
		}
		time.Sleep(*interval)
	}
}

func syncOnce(serverURL, token, routerConf string, wifiVlan int, logLevel string) error {
	// Wait until the VLAN bridges listed in the router config exist.
	rc, err := loadRouterConfig(routerConf)
	if err != nil {
		return err
	}
	var bridges []string
	for _, v := range rc.Vlans {
		bridges = append(bridges, "br-vlan"+strconv.Itoa(v.ID))
	}
	waitForBridges(bridges, 5*time.Minute)

	targets, err := buildTargets(rc, wifiVlan)
	if err != nil {
		return err
	}
	log.Printf("vlansync: targets: %+v", targets)

	c := &client{base: strings.TrimSuffix(serverURL, "/"), token: token, debug: logLevel == "debug"}

	// Subnets discovered by the daemon; retry until all our CIDRs show up.
	subnets, err := c.listSubnets()
	if err != nil {
		return err
	}
	byCIDR := map[string]subnet{}
	var siteID string
	missing := []string{}
	for _, s := range subnets {
		byCIDR[s.CIDR] = s
	}
	for _, t := range targets {
		for _, cidr := range t.CIDRs {
			s, ok := byCIDR[cidr]
			if !ok {
				missing = append(missing, cidr)
				continue
			}
			if siteID == "" {
				siteID = s.SiteID
			}
		}
	}
	if len(missing) > 0 {
		return fmt.Errorf("subnets not yet discovered by the daemon: %s — retrying", strings.Join(missing, ", "))
	}
	if siteID == "" {
		return fmt.Errorf("no site id found on target subnets")
	}

	// The organization id is required by the Vlan model; take it from
	// the organizations endpoint (single-org setups are the norm).
	orgID, err := c.firstOrgID()
	if err != nil {
		return fmt.Errorf("resolving organization id: %v", err)
	}

	// Interface name -> id, restricted to the daemon host.
	ifaceIDs, err := c.interfaceIDsForHost("bpi-r1")
	if err != nil {
		return fmt.Errorf("resolving interfaces: %v", err)
	}

	existingVlans, err := c.listVlans()
	if err != nil {
		return err
	}
	byNumber := map[int]vlan{}
	for _, v := range existingVlans {
		byNumber[v.VlanNumber] = v
	}

	for _, t := range targets {
		var ifaceUUIDs []string
		for _, p := range t.Ports {
			if id, ok := ifaceIDs[p]; ok {
				ifaceUUIDs = append(ifaceUUIDs, id)
			} else {
				log.Printf("vlansync: interface %q not found in scanopy (vlan %d)", p, t.Number)
			}
		}
		var subnetUUIDs []string
		for _, cidr := range t.CIDRs {
			if s, ok := byCIDR[cidr]; ok {
				subnetUUIDs = append(subnetUUIDs, s.ID)
			}
		}

		if old, ok := byNumber[t.Number]; ok {
			// Only update when something actually changed.
			if sameIDs(old.SubnetIDs, subnetUUIDs) && sameIDs(old.InterfaceIDs, ifaceUUIDs) && old.Name == t.Name {
				log.Printf("vlansync: vlan %d (%s) already up to date", t.Number, t.Name)
				continue
			}
			old.SubnetIDs = subnetUUIDs
			old.InterfaceIDs = ifaceUUIDs
			old.Name = t.Name
			if err := c.updateVlan(old); err != nil {
				return fmt.Errorf("updating vlan %d: %v", t.Number, err)
			}
			log.Printf("vlansync: updated vlan %d (%s): subnets=%v interfaces=%v", t.Number, t.Name, t.CIDRs, t.Ports)
			continue
		}
		if err := c.createVlan(vlan{
			VlanNumber:   t.Number,
			Name:         t.Name,
			SiteID:       siteID,
			OrgID:        orgID,
			Source:       "Manual",
			SubnetIDs:    subnetUUIDs,
			InterfaceIDs: ifaceUUIDs,
		}); err != nil {
			return fmt.Errorf("creating vlan %d: %v", t.Number, err)
		}
		log.Printf("vlansync: created vlan %d (%s): subnets=%v interfaces=%v", t.Number, t.Name, t.CIDRs, t.Ports)
	}
	return nil
}

func sameIDs(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	set := map[string]bool{}
	for _, x := range a {
		set[x] = true
	}
	for _, x := range b {
		if !set[x] {
			return false
		}
	}
	return true
}

func normalizeCIDR(addr string) (string, error) {
	_, ipnet, err := net.ParseCIDR(addr)
	if err != nil {
		return "", err
	}
	return ipnet.String(), nil
}

func buildTargets(rc *routerConfig, wifiVlan int) ([]vlanTarget, error) {
	var out []vlanTarget
	for _, v := range rc.Vlans {
		cidr, err := normalizeCIDR(v.Address)
		if err != nil {
			return nil, fmt.Errorf("vlan %d: parsing address %q: %v", v.ID, v.Address, err)
		}
		out = append(out, vlanTarget{Number: v.ID, Name: v.Name, CIDRs: []string{cidr}, Ports: v.Ports})
	}
	if wifiVlan != 0 && rc.Wifi.Enabled && rc.Wifi.Address != "" {
		cidr, err := normalizeCIDR(rc.Wifi.Address)
		if err != nil {
			return nil, fmt.Errorf("wifi: parsing address %q: %v", rc.Wifi.Address, err)
		}
		for i := range out {
			if out[i].Number == wifiVlan {
				out[i].CIDRs = append(out[i].CIDRs, cidr)
				break
			}
		}
	}
	return out, nil
}

func loadRouterConfig(path string) (*routerConfig, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("reading %s: %v", path, err)
	}
	rc := &routerConfig{}
	if err := json.Unmarshal(b, rc); err != nil {
		return nil, fmt.Errorf("parsing %s: %v", path, err)
	}
	return rc, nil
}

func waitForBridges(bridges []string, timeout time.Duration) {
	deadline := time.Now().Add(timeout)
	for {
		missing := []string{}
		for _, b := range bridges {
			if _, err := os.Stat(filepath.Join("/sys/class/net", b)); err != nil {
				missing = append(missing, b)
			}
		}
		if len(missing) == 0 {
			return
		}
		if time.Now().After(deadline) {
			log.Printf("vlansync: bridges still missing after %v: %s — continuing anyway", timeout, strings.Join(missing, ","))
			return
		}
		log.Printf("vlansync: waiting for bridges: %s", strings.Join(missing, ","))
		time.Sleep(3 * time.Second)
	}
}

func waitForNetwork() {
	deadline := time.Now().Add(5 * time.Minute)
	for {
		data, err := os.ReadFile("/proc/net/route")
		if err == nil {
			for _, line := range strings.Split(string(data), "\n")[1:] {
				f := strings.Fields(line)
				if len(f) >= 8 && f[1] == "00000000" && f[7] == "00000000" {
					return
				}
			}
		}
		if time.Now().After(deadline) {
			return
		}
		time.Sleep(2 * time.Second)
	}
}

func mustRead(path string) []byte {
	b, err := os.ReadFile(path)
	if err != nil {
		log.Fatalf("vlansync: reading %s: %v", path, err)
	}
	return b
}
