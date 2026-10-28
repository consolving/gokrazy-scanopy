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
	return fetchAll[subnet](c, "/api/v1/subnets")
}

type vlan struct {
	ID           string   `json:"id,omitempty"`
	VlanNumber   int      `json:"vlan_number"`
	Name         string   `json:"name"`
	Description  *string  `json:"description"`
	SiteID       string   `json:"site_id"`
	OrgID        string   `json:"organization_id"`
	Source       any      `json:"source,omitempty"`
	SubnetIDs    []string `json:"subnet_ids"`
	InterfaceIDs []string `json:"interface_ids"`
	Tags         []string `json:"tags"`
}

func (c *client) listVlans() ([]vlan, error) {
	return fetchAll[vlan](c, "/api/v1/vlans")
}

// fetchAll walks the paginated list endpoints (default page size 50).
func fetchAll[T any](c *client, path string) ([]T, error) {
	var all []T
	for page := 1; page < 100; page++ {
		var batch []T
		if err := c.do("GET", fmt.Sprintf("%s?page=%d", path, page), nil, &batch); err != nil {
			return nil, err
		}
		if len(batch) == 0 {
			break
		}
		all = append(all, batch...)
		if len(batch) < 50 {
			break
		}
	}
	return all, nil
}

func (c *client) createVlan(v vlan) error {
	return c.do("POST", "/api/v1/vlans", v, nil)
}

func (c *client) createSubnet(cidr, siteID, subnetType string) error {
	body := map[string]any{
		"cidr":                      cidr,
		"name":                      cidr,
		"site_id":                   siteID,
		"subnet_type":               subnetType,
		"source":                    map[string]any{"type": "Manual"},
		"description":               nil,
		"virtualization_service_id": nil,
		"tags":                      []string{},
	}
	return c.do("POST", "/api/v1/subnets", body, nil)
}

func (c *client) updateVlan(v vlan) error {
	return c.do("PUT", "/api/v1/vlans/"+v.ID, v, nil)
}

// orgIDForSite resolves the organization a site belongs to. The
// /api/v1/organizations endpoint requires an interactive user context
// which api keys do not carry, but sites expose it directly.
func (c *client) orgIDForSite(siteID string) (string, error) {
	var sites []struct {
		ID             string `json:"id"`
		OrganizationID string `json:"organization_id"`
	}
	if err := c.do("GET", "/api/v1/sites", nil, &sites); err != nil {
		return "", err
	}
	for _, s := range sites {
		if s.ID == siteID {
			if s.OrganizationID == "" {
				return "", fmt.Errorf("site %s has no organization_id", siteID)
			}
			return s.OrganizationID, nil
		}
	}
	return "", fmt.Errorf("site %s not found", siteID)
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
