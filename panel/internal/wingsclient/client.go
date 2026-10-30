// package wingsclient is the panel's HTTP client for talking to wings daemons.
// Each call carries the node's shared token in the X-KG-Token header.
package wingsclient

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"time"

	"kgpanel/panel/internal/store"
)

type Client struct {
	http *http.Client
}

func New() *Client {
	return &Client{http: &http.Client{Timeout: 15 * time.Second}}
}

func urlQuery(s string) string { return url.QueryEscape(s) }

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

// do performs an authenticated request to a wings endpoint and returns the
// raw response for streaming (must be closed by caller).
func (c *Client) do(n *store.Node, method, path string, body io.Reader) (*http.Response, error) {
	req, err := http.NewRequest(method, n.URL+path, body)
	if err != nil {
		return nil, err
	}
	req.Header.Set("X-KG-Token", n.Token)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, fmt.Errorf("wings unreachable: %w", err)
	}
	if resp.StatusCode >= 300 {
		defer resp.Body.Close()
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 1024))
		return nil, fmt.Errorf("wings %s: %s", resp.Status, string(b))
	}
	return resp, nil
}

// FilesList lists a server's file directory on wings.
func (c *Client) FilesList(n *store.Node, serverID, path string) ([]map[string]interface{}, string, error) {
	resp, err := c.do(n, http.MethodGet, "/v1/servers/"+serverID+"/files?path="+urlQuery(path), nil)
	if err != nil {
		return nil, "", err
	}
	defer resp.Body.Close()
	var out struct {
		Path    string                   `json:"path"`
		Entries []map[string]interface{} `json:"entries"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return nil, "", err
	}
	return out.Entries, out.Path, nil
}

// FilesUpload proxies a multipart upload to wings.
func (c *Client) FilesUpload(n *store.Node, serverID, path string, r io.Reader, ct string) error {
	req, err := http.NewRequest(http.MethodPost, n.URL+"/v1/servers/"+serverID+"/files/upload?path="+urlQuery(path), r)
	if err != nil {
		return err
	}
	req.Header.Set("X-KG-Token", n.Token)
	req.Header.Set("Content-Type", ct)
	resp, err := c.http.Do(req)
	if err != nil {
		return fmt.Errorf("wings unreachable: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 1024))
		return fmt.Errorf("wings %s: %s", resp.Status, string(b))
	}
	return nil
}

// FilesAction sends a JSON action (mkdir/delete) to wings.
func (c *Client) FilesAction(n *store.Node, serverID, action string, path string) error {
	body, _ := json.Marshal(map[string]string{"path": path})
	resp, err := c.do(n, http.MethodPost, "/v1/servers/"+serverID+"/files/"+action, bytes.NewReader(body))
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	return nil
}

// FilesDownload proxies a file download from wings (caller closes body).
func (c *Client) FilesDownload(n *store.Node, serverID, path string) (*http.Response, error) {
	return c.do(n, http.MethodGet, "/v1/servers/"+serverID+"/files/download?path="+urlQuery(path), nil)
}

// BackupsList lists backups on wings.
func (c *Client) BackupsList(n *store.Node, serverID string) ([]map[string]interface{}, error) {
	resp, err := c.do(n, http.MethodGet, "/v1/servers/"+serverID+"/backups", nil)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	var out struct {
		Backups []map[string]interface{} `json:"backups"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return nil, err
	}
	return out.Backups, nil
}

// BackupCreate triggers a backup on wings.
func (c *Client) BackupCreate(n *store.Node, serverID string) error {
	resp, err := c.do(n, http.MethodPost, "/v1/servers/"+serverID+"/backups/create", bytes.NewReader([]byte("{}")))
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	return nil
}

// BackupDownload proxies a backup download (caller closes body).
func (c *Client) BackupDownload(n *store.Node, serverID, name string) (*http.Response, error) {
	return c.do(n, http.MethodGet, "/v1/servers/"+serverID+"/backups/download?name="+urlQuery(name), nil)
}

// BackupAction sends a JSON action (delete/restore) for a backup.
func (c *Client) BackupAction(n *store.Node, serverID, action, name string) error {
	body, _ := json.Marshal(map[string]string{"name": name})
	resp, err := c.do(n, http.MethodPost, "/v1/servers/"+serverID+"/backups/"+action, bytes.NewReader(body))
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	return nil
}
