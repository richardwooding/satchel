package bell

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// Client rings and listens against one bell server.
type Client struct {
	Base string // e.g. https://satchel-send.fly.dev
	HTTP *http.Client
}

// FromRelay derives the bell's base URL from a relay WebSocket URL: both are
// served by the same satchel-relay.
func FromRelay(relayURL string) (string, error) {
	u, err := url.Parse(relayURL)
	if err != nil {
		return "", err
	}
	switch u.Scheme {
	case "wss":
		u.Scheme = "https"
	case "ws":
		u.Scheme = "http"
	default:
		return "", fmt.Errorf("bell: relay URL %q is not ws(s)", relayURL)
	}
	u.Path, u.RawQuery, u.Fragment = "", "", ""
	return strings.TrimSuffix(u.String(), "/"), nil
}

func (c *Client) client() *http.Client {
	if c.HTTP != nil {
		return c.HTTP
	}
	return http.DefaultClient
}

// Ring posts data to an inbox.
func (c *Client) Ring(ctx context.Context, id ID, data []byte) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.Base+"/bell/"+id.String(), bytes.NewReader(data))
	if err != nil {
		return err
	}
	resp, err := c.client().Do(req)
	if err != nil {
		return err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusAccepted {
		return fmt.Errorf("bell: ring refused: %s", resp.Status)
	}
	return nil
}

// Poll waits for rings on ids, returning none after the server's wait.
func (c *Client) Poll(ctx context.Context, ids []ID) ([]Ring, error) {
	hexes := make([]string, len(ids))
	for i, id := range ids {
		hexes[i] = id.String()
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.Base+"/bell?ids="+strings.Join(hexes, ","), nil)
	if err != nil {
		return nil, err
	}
	resp, err := c.client().Do(req)
	if err != nil {
		return nil, err
	}
	defer func() { _ = resp.Body.Close() }()
	switch resp.StatusCode {
	case http.StatusNoContent:
		return nil, nil
	case http.StatusOK:
		var rings []Ring
		if err := json.NewDecoder(resp.Body).Decode(&rings); err != nil {
			return nil, err
		}
		return rings, nil
	}
	return nil, fmt.Errorf("bell: poll refused: %s", resp.Status)
}

// Listen polls until ctx ends, handing each ring to onRing. ids is asked
// afresh before every poll, so a daily rotation takes effect by itself.
// Failures back off up to a minute; they are not fatal.
func (c *Client) Listen(ctx context.Context, ids func() []ID, onRing func(Ring)) {
	wait := time.Second
	for ctx.Err() == nil {
		pctx, cancel := context.WithTimeout(ctx, 70*time.Second)
		rings, err := c.Poll(pctx, ids())
		cancel()
		if err != nil {
			select {
			case <-ctx.Done():
				return
			case <-time.After(wait):
			}
			wait = min(2*wait, time.Minute)
			continue
		}
		wait = time.Second
		for _, r := range rings {
			onRing(r)
		}
	}
}
