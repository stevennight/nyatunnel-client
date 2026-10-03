// Package enroll talks to the server's public enrollment endpoints (docs/协议.md §3).
package enroll

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"runtime"
	"time"
)

// Preview is what the device is about to join.
type Preview struct {
	ServerName     string `json:"serverName"`
	Owner          string `json:"owner"`
	DeviceNameHint string `json:"deviceNameHint"`
	Tunnels        []struct {
		Name      string `json:"name"`
		PublicURL string `json:"publicUrl"`
	} `json:"tunnels"`
	ExpiresAt int64 `json:"expiresAt"`
}

// Error is an error response of the server.
type Error struct {
	Status  int
	Code    string `json:"error"`
	Message string `json:"message"`
}

func (e *Error) Error() string {
	if e.Message != "" {
		return e.Message
	}
	return fmt.Sprintf("server answered %d %s", e.Status, e.Code)
}

var httpClient = &http.Client{Timeout: 20 * time.Second}

func do(ctx context.Context, method, rawURL string, body, out any) error {
	var rd *bytes.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		rd = bytes.NewReader(b)
	} else {
		rd = bytes.NewReader(nil)
	}
	req, err := http.NewRequestWithContext(ctx, method, rawURL, rd)
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := httpClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		e := &Error{Status: resp.StatusCode}
		_ = json.NewDecoder(resp.Body).Decode(e)
		return e
	}
	return json.NewDecoder(resp.Body).Decode(out)
}

// GetPreview asks what an enroll code would give, without consuming it.
func GetPreview(ctx context.Context, server, code string) (*Preview, error) {
	var p Preview
	if err := do(ctx, http.MethodGet, server+"/api/v1/enroll/preview?code="+url.QueryEscape(code), nil, &p); err != nil {
		return nil, err
	}
	return &p, nil
}

// Claim registers pub under code and returns the new device id.
func Claim(ctx context.Context, server, code, deviceName, clientVersion string, pub ed25519.PublicKey, gui bool) (string, error) {
	var out struct {
		DeviceID string `json:"deviceId"`
	}
	err := do(ctx, http.MethodPost, server+"/api/v1/enroll/claim", map[string]any{
		"code": code, "publicKey": base64.StdEncoding.EncodeToString(pub), "deviceName": deviceName,
		"platform": runtime.GOOS + "/" + runtime.GOARCH, "clientVersion": clientVersion, "gui": gui,
	}, &out)
	return out.DeviceID, err
}
