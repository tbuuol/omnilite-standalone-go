package rpc

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
)

// fakeNode answers like litecoind: results with HTTP 200, errors with HTTP 500.
func fakeNode(t *testing.T, handle func(method string, params []json.RawMessage) (any, *Error)) *Client {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if u, p, _ := r.BasicAuth(); u != "user" || p != "pass" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		var req struct {
			ID     uint64            `json:"id"`
			Method string            `json:"method"`
			Params []json.RawMessage `json:"params"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Error(err)
			return
		}
		result, rpcErr := handle(req.Method, req.Params)
		if rpcErr != nil {
			w.WriteHeader(http.StatusInternalServerError)
		}
		json.NewEncoder(w).Encode(map[string]any{"result": result, "error": rpcErr, "id": req.ID})
	}))
	t.Cleanup(srv.Close)
	return New(srv.URL, "user", "pass")
}

func TestCallErrors(t *testing.T) {
	c := fakeNode(t, func(method string, _ []json.RawMessage) (any, *Error) {
		return nil, &Error{Code: CodeInvalidParameter, Message: "Block height out of range"}
	})
	_, err := c.GetBlockHash(context.Background(), 99)
	if !IsCode(err, CodeInvalidParameter) {
		t.Fatalf("err = %v, want code %d", err, CodeInvalidParameter)
	}

	bad := New(c.url, "user", "wrong")
	if _, err := bad.GetBlockCount(context.Background()); !errors.Is(err, ErrUnauthorized) {
		t.Fatalf("err = %v, want ErrUnauthorized", err)
	}
}

func TestTxIndex(t *testing.T) {
	ctx := context.Background()
	tests := []struct {
		name          string
		handle        func(string, []json.RawMessage) (any, *Error)
		enabled, sync bool
	}{
		{
			name: "getindexinfo synced",
			handle: func(m string, _ []json.RawMessage) (any, *Error) {
				return map[string]any{"txindex": map[string]any{"synced": true, "best_block_height": 10}}, nil
			},
			enabled: true, sync: true,
		},
		{
			name: "getindexinfo without txindex",
			handle: func(m string, _ []json.RawMessage) (any, *Error) {
				return map[string]any{}, nil
			},
		},
		{
			name:    "old node with txindex",
			handle:  oldNode(true),
			enabled: true, sync: true,
		},
		{
			name:   "old node without txindex",
			handle: oldNode(false),
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			enabled, synced, err := fakeNode(t, tt.handle).TxIndex(ctx)
			if err != nil {
				t.Fatal(err)
			}
			if enabled != tt.enabled || synced != tt.sync {
				t.Fatalf("got enabled=%v synced=%v, want %v %v", enabled, synced, tt.enabled, tt.sync)
			}
		})
	}
}

// oldNode behaves like a node before 0.21, which has no getindexinfo.
func oldNode(txindex bool) func(string, []json.RawMessage) (any, *Error) {
	return func(method string, _ []json.RawMessage) (any, *Error) {
		switch method {
		case "getindexinfo":
			return nil, &Error{Code: CodeMethodNotFound, Message: "Method not found"}
		case "getblockhash":
			return "blockhash1", nil
		case "getblock":
			return map[string]any{"tx": []string{"coinbase1"}}, nil
		case "getrawtransaction":
			if txindex {
				return "00", nil
			}
			return nil, &Error{Code: CodeInvalidAddressKey, Message: "No such mempool transaction. Use -txindex"}
		}
		return nil, &Error{Code: CodeMethodNotFound, Message: method}
	}
}
