// package wingsclient is the panel's HTTP client for talking to wings daemons.
// Each call carries the node's shared token in the X-KG-Token header.
package wingsclient

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"

	"kgpanel/panel/internal/store"
)

type Client struct {
	http *http.Client
}

func New() *Client {
	return &Client{http: &http.Client{Timeout: 15 * time.Second}}
}

// Power tells a node's wings to start/stop/restart/kill a server container.
func (c *Client) Power(n *store.Node, serverID, signal string) error {
	body, _ := json.Marshal(map[string]string{"signal": signal})
	req, err := http.NewRequest(http.MethodPost, n.URL+"/v1/servers/"+serverID+"/power", bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-KG-Token", n.Token)
	resp, err := c.http.Do(req)
	if err != nil {
		return fmt.Errorf("wings unreachable: %w", err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(io.LimitReader(resp.Body, 1024))
	if resp.StatusCode >= 300 {
		return fmt.Errorf("wings %s: %s", resp.Status, string(b))
	}
	return nil
}

// Status asks wings what the container is actually doing.
func (c *Client) Status(n *store.Node, serverID string) (string, error) {
	req, err := http.NewRequest(http.MethodGet, n.URL+"/v1/servers/"+serverID+"/status", nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("X-KG-Token", n.Token)
	resp, err := c.http.Do(req)
	if err != nil {
		return "", fmt.Errorf("wings unreachable: %w", err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
	if resp.StatusCode >= 300 {
		return "", fmt.Errorf("wings %s: %s", resp.Status, string(b))
	}
	var out struct {
		State string `json:"state"`
	}
	if err := json.Unmarshal(b, &out); err != nil {
		return "", fmt.Errorf("bad wings response: %w", err)
	}
	return out.State, nil
}

// ConsoleURL returns the ws:// url for a server's live console stream.
func (c *Client) ConsoleURL(n *store.Node, serverID string) string {
	base := n.URL
	if len(base) >= 5 && base[:5] == "https" {
		return "wss" + base[5:] + "/v1/servers/" + serverID + "/console"
	}
	return "ws" + base[4:] + "/v1/servers/" + serverID + "/console"
}
