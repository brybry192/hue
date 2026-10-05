package hue

import (
	"bytes"
	"context"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
	"time"
)

const clipBase = "/clip/v2/resource/"

// DefaultTimeout is the per-request timeout when none is configured.
const DefaultTimeout = 10 * time.Second

// ErrLinkButton is returned by CreateAppKey when the bridge's link button has
// not been pressed yet. It is the expected error while polling.
var ErrLinkButton = errors.New("link button not pressed")

// ErrUnauthorized means the bridge rejected the application key.
var ErrUnauthorized = errors.New("bridge rejected the application key")

// Options configures a Client.
type Options struct {
	Host string // bridge IP or hostname
	// AppKey is the hue-application-key sent with every CLIP v2 request.
	AppKey string
	// CertSHA256 pins the bridge's leaf certificate (hex SHA-256 of the DER
	// bytes). Empty means no pinning.
	CertSHA256 string
	// Insecure disables pinning entirely.
	Insecure bool
	Timeout  time.Duration
}

// Client talks to one bridge.
type Client struct {
	host   string
	appKey string
	hc     *http.Client
}

// New builds a Client. It does not contact the bridge.
func New(o Options) *Client {
	timeout := o.Timeout
	if timeout <= 0 {
		timeout = DefaultTimeout
	}
	tr := &http.Transport{
		TLSClientConfig:   tlsConfig(o.CertSHA256, o.Insecure),
		DialContext:       (&net.Dialer{Timeout: timeout}).DialContext,
		ForceAttemptHTTP2: true,
	}
	return &Client{
		host:   o.Host,
		appKey: o.AppKey,
		hc:     &http.Client{Transport: tr, Timeout: timeout},
	}
}

// Host reports the bridge address this client targets.
func (c *Client) Host() string { return c.host }

// tlsConfig builds a TLS config for the bridge.
//
// A Hue bridge serves a self-signed certificate for an IP address, so the
// system trust store can never validate it. Rather than trusting anything on
// the network, we skip the stock verification and pin the exact leaf
// certificate recorded at `hue auth` time (trust on first use).
func tlsConfig(pin string, insecure bool) *tls.Config {
	cfg := &tls.Config{
		InsecureSkipVerify: true, //nolint:gosec // replaced by the pin check below
		MinVersion:         tls.VersionTLS12,
	}
	if insecure || pin == "" {
		return cfg
	}
	want := strings.ToLower(strings.TrimSpace(pin))
	cfg.VerifyPeerCertificate = func(rawCerts [][]byte, _ [][]*x509.Certificate) error {
		for _, raw := range rawCerts {
			sum := sha256.Sum256(raw)
			if hex.EncodeToString(sum[:]) == want {
				return nil
			}
		}
		return fmt.Errorf("bridge certificate does not match the pinned fingerprint %s; "+
			"re-run 'hue auth' if you replaced the bridge, or set bridge.insecure", want)
	}
	return cfg
}

// Fingerprint dials the bridge and returns the hex SHA-256 of its leaf
// certificate, for pinning.
func Fingerprint(ctx context.Context, host string, timeout time.Duration) (string, error) {
	if timeout <= 0 {
		timeout = DefaultTimeout
	}
	d := &tls.Dialer{
		NetDialer: &net.Dialer{Timeout: timeout},
		Config:    &tls.Config{InsecureSkipVerify: true, MinVersion: tls.VersionTLS12}, //nolint:gosec // fingerprinting
	}
	conn, err := d.DialContext(ctx, "tcp", net.JoinHostPort(host, "443"))
	if err != nil {
		return "", fmt.Errorf("connect to bridge at %s: %w", host, err)
	}
	defer conn.Close()

	tc, ok := conn.(*tls.Conn)
	if !ok {
		return "", fmt.Errorf("unexpected connection type %T", conn)
	}
	certs := tc.ConnectionState().PeerCertificates
	if len(certs) == 0 {
		return "", errors.New("bridge presented no certificate")
	}
	sum := sha256.Sum256(certs[0].Raw)
	return hex.EncodeToString(sum[:]), nil
}

// clipError is one entry of a CLIP v2 response's errors array.
type clipError struct {
	Description string `json:"description"`
}

// do performs a request with retries on transient failures. body is marshalled
// to JSON when non-nil.
func (c *Client) do(ctx context.Context, method, path string, body any, withKey bool) ([]byte, error) {
	if c.host == "" {
		return nil, errors.New("no bridge configured; run 'hue auth'")
	}
	var payload []byte
	if body != nil {
		var err error
		if payload, err = json.Marshal(body); err != nil {
			return nil, fmt.Errorf("encode request: %w", err)
		}
	}

	url := "https://" + c.host + path
	const attempts = 4
	var lastErr error
	for attempt := range attempts {
		if attempt > 0 {
			// The bridge is a small embedded device; back off rather than
			// hammering it.
			delay := time.Duration(1<<attempt) * 150 * time.Millisecond
			select {
			case <-ctx.Done():
				return nil, ctx.Err()
			case <-time.After(delay):
			}
		}

		var rdr io.Reader
		if payload != nil {
			rdr = bytes.NewReader(payload)
		}
		req, err := http.NewRequestWithContext(ctx, method, url, rdr)
		if err != nil {
			return nil, err
		}
		req.Header.Set("Accept", "application/json")
		if payload != nil {
			req.Header.Set("Content-Type", "application/json")
		}
		if withKey {
			if c.appKey == "" {
				return nil, errors.New("no application key configured; run 'hue auth'")
			}
			req.Header.Set("hue-application-key", c.appKey)
		}

		resp, err := c.hc.Do(req)
		if err != nil {
			lastErr = err
			continue
		}
		data, readErr := io.ReadAll(resp.Body)
		resp.Body.Close()
		if readErr != nil {
			lastErr = readErr
			continue
		}

		switch {
		case resp.StatusCode == http.StatusTooManyRequests, resp.StatusCode >= 500:
			lastErr = fmt.Errorf("bridge returned %s: %s", resp.Status, snippet(data))
			continue
		case resp.StatusCode == http.StatusUnauthorized, resp.StatusCode == http.StatusForbidden:
			return nil, fmt.Errorf("%w (%s); run 'hue auth'", ErrUnauthorized, resp.Status)
		case resp.StatusCode >= 400:
			return nil, fmt.Errorf("bridge returned %s: %s", resp.Status, snippet(data))
		}
		return data, nil
	}
	return nil, fmt.Errorf("bridge request %s %s failed after %d attempts: %w", method, path, attempts, lastErr)
}

func snippet(b []byte) string {
	s := strings.TrimSpace(string(b))
	if len(s) > 240 {
		s = s[:240] + "..."
	}
	if s == "" {
		return "(empty response)"
	}
	return s
}

// fetch lists every resource of a given CLIP v2 type.
func fetch[T any](ctx context.Context, c *Client, rtype string) ([]T, error) {
	data, err := c.do(ctx, http.MethodGet, clipBase+rtype, nil, true)
	if err != nil {
		return nil, fmt.Errorf("list %s: %w", rtype, err)
	}
	var env struct {
		Errors []clipError `json:"errors"`
		Data   []T         `json:"data"`
	}
	if err := json.Unmarshal(data, &env); err != nil {
		return nil, fmt.Errorf("decode %s response: %w", rtype, err)
	}
	if len(env.Errors) > 0 {
		return nil, fmt.Errorf("list %s: %s", rtype, env.Errors[0].Description)
	}
	return env.Data, nil
}

// Rooms lists every room.
func (c *Client) Rooms(ctx context.Context) ([]Group, error) {
	return fetch[Group](ctx, c, "room")
}

// Zones lists every zone.
func (c *Client) Zones(ctx context.Context) ([]Group, error) {
	return fetch[Group](ctx, c, "zone")
}

// Devices lists every device.
func (c *Client) Devices(ctx context.Context) ([]Device, error) {
	return fetch[Device](ctx, c, "device")
}

// Lights lists every light service.
func (c *Client) Lights(ctx context.Context) ([]Light, error) {
	return fetch[Light](ctx, c, "light")
}

// Motions lists every motion service.
func (c *Client) Motions(ctx context.Context) ([]Motion, error) {
	return fetch[Motion](ctx, c, "motion")
}

// Connectivity lists the Zigbee connectivity status of every device.
func (c *Client) Connectivity(ctx context.Context) ([]ZigbeeConnectivity, error) {
	return fetch[ZigbeeConnectivity](ctx, c, "zigbee_connectivity")
}

// GroupedLights lists every grouped-light service.
func (c *Client) GroupedLights(ctx context.Context) ([]GroupedLight, error) {
	return fetch[GroupedLight](ctx, c, "grouped_light")
}

// Bridge returns the bridge's own resource.
func (c *Client) Bridge(ctx context.Context) (BridgeInfo, error) {
	got, err := fetch[BridgeInfo](ctx, c, "bridge")
	if err != nil {
		return BridgeInfo{}, err
	}
	if len(got) == 0 {
		return BridgeInfo{}, errors.New("bridge returned no bridge resource")
	}
	return got[0], nil
}

// RawResource is the common envelope of any CLIP v2 resource, enough to
// inventory a bridge without modelling every type.
type RawResource struct {
	ID       string      `json:"id"`
	Type     string      `json:"type"`
	Metadata Metadata    `json:"metadata"`
	Owner    ResourceRef `json:"owner"`
}

// AllResources lists every resource on the bridge, whatever its type. This is
// how new firmware or new hardware capabilities get discovered rather than
// guessed at.
func (c *Client) AllResources(ctx context.Context) ([]RawResource, error) {
	data, err := c.do(ctx, http.MethodGet, "/clip/v2/resource", nil, true)
	if err != nil {
		return nil, fmt.Errorf("list resources: %w", err)
	}
	var env struct {
		Errors []clipError   `json:"errors"`
		Data   []RawResource `json:"data"`
	}
	if err := json.Unmarshal(data, &env); err != nil {
		return nil, fmt.Errorf("decode resource list: %w", err)
	}
	if len(env.Errors) > 0 {
		return nil, fmt.Errorf("list resources: %s", env.Errors[0].Description)
	}
	return env.Data, nil
}

// RawType returns the bridge's untouched JSON for one resource type, for
// inspecting fields this client does not model.
func (c *Client) RawType(ctx context.Context, rtype string) ([]byte, error) {
	data, err := c.do(ctx, http.MethodGet, clipBase+rtype, nil, true)
	if err != nil {
		return nil, fmt.Errorf("list %s: %w", rtype, err)
	}
	return data, nil
}

// put sends a state change and surfaces errors reported in the 200 body.
func (c *Client) put(ctx context.Context, rtype, id string, body any) error {
	data, err := c.do(ctx, http.MethodPut, clipBase+rtype+"/"+id, body, true)
	if err != nil {
		return err
	}
	var env struct {
		Errors []clipError `json:"errors"`
	}
	if err := json.Unmarshal(data, &env); err != nil {
		return fmt.Errorf("decode %s update response: %w", rtype, err)
	}
	if len(env.Errors) > 0 {
		return fmt.Errorf("update %s %s: %s", rtype, id, env.Errors[0].Description)
	}
	return nil
}

// SetLightOn switches a single light service on or off.
func (c *Client) SetLightOn(ctx context.Context, id string, on bool) error {
	return c.put(ctx, "light", id, map[string]any{"on": OnState{On: on}})
}

// SetGroupOn switches every light in a grouped-light service on or off.
func (c *Client) SetGroupOn(ctx context.Context, groupedLightID string, on bool) error {
	return c.put(ctx, "grouped_light", groupedLightID, map[string]any{"on": OnState{On: on}})
}
