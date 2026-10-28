package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"
)

// client is a minimal Scanopy REST API client for the endpoints
// vlansync needs (subnets, vlans, hosts, interfaces, organizations).
type client struct {
	base  string
	token string
	debug bool
	http  *http.Client
}

func (c *client) do(method, path string, body any, out any) error {
	var rdr io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return err
		}
		rdr = bytes.NewReader(b)
	}
	req, err := http.NewRequest(method, c.base+path, rdr)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+c.token)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if c.http == nil {
		c.http = &http.Client{Timeout: 30 * time.Second}
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(resp.Body)
	if err != nil {
		return err
	}
	if resp.StatusCode >= 300 {
		return fmt.Errorf("%s %s: HTTP %d: %s", method, path, resp.StatusCode, string(data[:min(len(data), 300)]))
	}
	if out != nil {
		var envelope struct {
			Success bool            `json:"success"`
			Data    json.RawMessage `json:"data"`
		}
		if err := json.Unmarshal(data, &envelope); err == nil && envelope.Data != nil {
			return json.Unmarshal(envelope.Data, out)
		}
		return json.Unmarshal(data, out)
	}
	return nil
}

type subnet struct {
	ID     string `json:"id"`
	CIDR   string `json:"cidr"`
	SiteID string `json:"site_id"`
}

func (c *client) listSubnets() ([]subnet, error) {
	var out []subnet
	err := c.do("GET", "/api/v1/subnets", nil, &out)
	return out, err
}

type vlan struct {
	ID           string   `json:"id,omitempty"`
	VlanNumber   int      `json:"vlan_number"`
	Name         string   `json:"name"`
	Description  *string  `json:"description,omitempty"`
	SiteID       string   `json:"site_id"`
	OrgID        string   `json:"organization_id"`
	Source       string   `json:"source"`
	SubnetIDs    []string `json:"subnet_ids"`
	InterfaceIDs []string `json:"interface_ids"`
}

func (c *client) listVlans() ([]vlan, error) {
	var out []vlan
	err := c.do("GET", "/api/v1/vlans", nil, &out)
	return out, err
}

func (c *client) createVlan(v vlan) error {
	return c.do("POST", "/api/v1/vlans", v, nil)
}

func (c *client) updateVlan(v vlan) error {
	return c.do("PUT", "/api/v1/vlans/"+v.ID, v, nil)
}

func (c *client) firstOrgID() (string, error) {
	var orgs []struct {
		ID string `json:"id"`
	}
	if err := c.do("GET", "/api/v1/organizations", nil, &orgs); err != nil {
		return "", err
	}
	if len(orgs) == 0 {
		return "", fmt.Errorf("no organizations visible for this api key")
	}
	return orgs[0].ID, nil
}

type host struct {
	ID       string `json:"id"`
	Hostname string `json:"hostname"`
}

type iface struct {
	ID     string `json:"id"`
	Name   string `json:"name"`
	HostID string `json:"host_id"`
}

// interfaceIDsForHost resolves interface names (lan1, ...) to their
// ids for the given host, identified by its hostname prefix.
func (c *client) interfaceIDsForHost(hostPrefix string) (map[string]string, error) {
	var hosts []host
	if err := c.do("GET", "/api/v1/hosts", nil, &hosts); err != nil {
		return nil, err
	}
	var hostID string
	for _, h := range hosts {
		if len(h.Hostname) >= len(hostPrefix) && h.Hostname[:len(hostPrefix)] == hostPrefix {
			hostID = h.ID
			break
		}
	}
	if hostID == "" {
		return nil, fmt.Errorf("no host with hostname prefix %q found in scanopy", hostPrefix)
	}
	var ifaces []iface
	if err := c.do("GET", "/api/v1/interfaces", nil, &ifaces); err != nil {
		return nil, err
	}
	out := map[string]string{}
	for _, i := range ifaces {
		if i.HostID == hostID {
			out[i.Name] = i.ID
		}
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("no interfaces found for host %q", hostPrefix)
	}
	return out, nil
}
