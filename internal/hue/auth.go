package hue

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// DiscoveryURL is Signify's bridge discovery service. It reports bridges that
// share the caller's public IP.
const DiscoveryURL = "https://discovery.meethue.com"

// DiscoveredBridge is one entry from the discovery service.
type DiscoveredBridge struct {
	ID              string `json:"id"`
	InternalAddress string `json:"internalipaddress"`
	Port            int    `json:"port"`
}

// Discover asks the discovery service which bridges live on this network.
//
// It requires outbound internet access and only sees bridges behind the same
// public IP, so callers should always allow an address to be given explicitly.
func Discover(ctx context.Context, timeout time.Duration) ([]DiscoveredBridge, error) {
	if timeout <= 0 {
		timeout = DefaultTimeout
	}
	return discoverFrom(ctx, DiscoveryURL, timeout)
}

func discoverFrom(ctx context.Context, endpoint string, timeout time.Duration) ([]DiscoveredBridge, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/json")

	// The discovery service is a normal public HTTPS endpoint, so it gets
	// ordinary certificate verification.
	hc := &http.Client{Timeout: timeout}
	resp, err := hc.Do(req)
	if err != nil {
		return nil, fmt.Errorf("query %s: %w", endpoint, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("discovery service returned %s", resp.Status)
	}
	var out []DiscoveredBridge
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return nil, fmt.Errorf("decode discovery response: %w", err)
	}
	return out, nil
}

// appKeyResponse is the legacy /api reply shape, which is still how an
// application key is minted for CLIP v2.
type appKeyResponse []struct {
	Success *struct {
		Username  string `json:"username"`
		ClientKey string `json:"clientkey"`
	} `json:"success"`
	Error *struct {
		Type        int    `json:"type"`
		Description string `json:"description"`
	} `json:"error"`
}

// linkButtonErrType is the bridge's "link button not pressed" error code.
const linkButtonErrType = 101

// CreateAppKey asks the bridge for a new application key. The bridge's link
// button must have been pressed within the last ~30 seconds, otherwise
// ErrLinkButton is returned and the caller should retry.
//
// This uses the bridge's /api endpoint, which remains the documented way to
// mint a key for the CLIP v2 API.
func (c *Client) CreateAppKey(ctx context.Context, deviceType string) (string, error) {
	body := map[string]any{
		"devicetype":        deviceType,
		"generateclientkey": true,
	}
	data, err := c.do(ctx, http.MethodPost, "/api", body, false)
	if err != nil {
		return "", err
	}
	var out appKeyResponse
	if err := json.Unmarshal(data, &out); err != nil {
		return "", fmt.Errorf("decode pairing response: %w", err)
	}
	if len(out) == 0 {
		return "", errors.New("bridge returned an empty pairing response")
	}
	entry := out[0]
	if entry.Error != nil {
		if entry.Error.Type == linkButtonErrType {
			return "", ErrLinkButton
		}
		return "", fmt.Errorf("bridge refused pairing: %s", entry.Error.Description)
	}
	if entry.Success == nil || entry.Success.Username == "" {
		return "", errors.New("bridge returned no application key")
	}
	return entry.Success.Username, nil
}

// NormalizeHost strips a scheme, path and default port from user input so that
// both "192.168.1.5" and "https://192.168.1.5/" work.
func NormalizeHost(in string) string {
	s := strings.TrimSpace(in)
	if s == "" {
		return ""
	}
	if strings.Contains(s, "://") {
		if u, err := url.Parse(s); err == nil && u.Host != "" {
			s = u.Host
		}
	}
	s = strings.TrimSuffix(s, "/")
	s = strings.TrimSuffix(s, ":443")
	return s
}
