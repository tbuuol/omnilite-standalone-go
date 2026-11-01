// Package rpc is a minimal JSON-RPC client for litecoind.
//
// Only standard Litecoin Core RPCs are used, so the indexer works with any
// node version that offers them.
package rpc

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"sync/atomic"
	"time"
)

// Error is an error returned by the node itself.
type Error struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

func (e *Error) Error() string { return fmt.Sprintf("rpc error %d: %s", e.Code, e.Message) }

// Error codes from Litecoin Core (src/rpc/protocol.h).
const (
	CodeMethodNotFound    = -32601
	CodeInvalidAddressKey = -5 // also "no such transaction"
	CodeInvalidParameter  = -8
	CodeInWarmup          = -28
)

// IsCode reports whether err is a node error with the given code.
func IsCode(err error, code int) bool {
	var e *Error
	return errors.As(err, &e) && e.Code == code
}

// ErrUnauthorized is returned when the node rejects the credentials.
var ErrUnauthorized = errors.New("rpc: unauthorized, check rpcuser/rpcpassword or the cookie file")

type Client struct {
	url  string
	user string
	pass string
	http *http.Client
	id   atomic.Uint64
}

// New creates a client for url (e.g. "http://127.0.0.1:9332").
func New(url, user, pass string) *Client {
	return &Client{
		url:  url,
		user: user,
		pass: pass,
		http: &http.Client{Timeout: 2 * time.Minute},
	}
}

// Call invokes method and decodes the result into result (which may be nil).
func (c *Client) Call(ctx context.Context, method string, result any, params ...any) error {
	if params == nil {
		params = []any{}
	}
	body, err := json.Marshal(map[string]any{
		"jsonrpc": "1.0",
		"id":      c.id.Add(1),
		"method":  method,
		"params":  params,
	})
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.url, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	req.SetBasicAuth(c.user, c.pass)

	resp, err := c.http.Do(req)
	if err != nil {
		return fmt.Errorf("rpc %s: %w", method, err)
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(resp.Body)
	if err != nil {
		return fmt.Errorf("rpc %s: %w", method, err)
	}
	if resp.StatusCode == http.StatusUnauthorized {
		return ErrUnauthorized
	}

	// litecoind answers RPC errors with HTTP 500 and a JSON body, so the
	// status code alone says nothing; try the body first.
	var r struct {
		Result json.RawMessage `json:"result"`
		Error  *Error          `json:"error"`
	}
	if err := json.Unmarshal(data, &r); err != nil {
		if len(data) > 200 {
			data = data[:200]
		}
		return fmt.Errorf("rpc %s: HTTP %d: %s", method, resp.StatusCode, bytes.TrimSpace(data))
	}
	if r.Error != nil {
		return r.Error
	}
	if result == nil {
		return nil
	}
	if err := json.Unmarshal(r.Result, result); err != nil {
		return fmt.Errorf("rpc %s: decode result: %w", method, err)
	}
	return nil
}
