// Package api serves a JSON-RPC interface compatible with the omni_* calls
// of omnilitecored. A few read-only Litecoin calls are passed through to
// the node, so clients can use this server in place of omnilitecored.
package api

import (
	"bytes"
	"context"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"strings"

	"github.com/tbuuol/omnilite-standalone/internal/chain"
	"github.com/tbuuol/omnilite-standalone/internal/rpc"
	"github.com/tbuuol/omnilite-standalone/internal/store"
)

// RPC error codes of Litecoin Core (src/rpc/protocol.h).
const (
	codeMisc            = -1
	codeType            = -3
	codeInvalidAddress  = -5
	codeInvalidParam    = -8
	codeDatabase        = -20
	codeInvalidRequest  = -32600
	codeMethodNotFound  = -32601
	codeInternal        = -32603
	codeParse           = -32700
	maxRequestBodyBytes = 1 << 20
)

// Error is a JSON-RPC error.
type Error struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

func (e *Error) Error() string { return e.Message }

func errorf(code int, msg string) *Error { return &Error{Code: code, Message: msg} }

// Node is what the server needs from litecoind.
type Node interface {
	Call(ctx context.Context, method string, result any, params ...any) error
}

// Server answers JSON-RPC requests.
type Server struct {
	Store  *store.Store
	Params *chain.Params
	Node   Node
	User   string
	Pass   string
	Log    *slog.Logger
}

// passThrough lists the read-only node calls forwarded to litecoind.
var passThrough = map[string]bool{
	"getrawtransaction": true, "decoderawtransaction": true, "decodescript": true,
	"getblock": true, "getblockhash": true, "getblockheader": true, "getblockcount": true,
	"getbestblockhash": true, "getblockchaininfo": true, "getnetworkinfo": true,
	"getrawmempool": true, "getmempoolinfo": true, "getmempoolentry": true,
	"gettxout": true, "estimatesmartfee": true, "validateaddress": true,
}

type request struct {
	ID     json.RawMessage   `json:"id"`
	Method string            `json:"method"`
	Params []json.RawMessage `json:"params"`
}

type response struct {
	Result any             `json:"result"`
	Error  *Error          `json:"error"`
	ID     json.RawMessage `json:"id"`
}

func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	user, pass, ok := r.BasicAuth()
	if !ok || subtle.ConstantTimeCompare([]byte(user), []byte(s.User)) != 1 ||
		subtle.ConstantTimeCompare([]byte(pass), []byte(s.Pass)) != 1 {
		w.Header().Set("WWW-Authenticate", `Basic realm="jsonrpc"`)
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	if r.Method != http.MethodPost {
		http.Error(w, "JSONRPC server handles only POST requests", http.StatusMethodNotAllowed)
		return
	}
	body, err := io.ReadAll(io.LimitReader(r.Body, maxRequestBodyBytes))
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	body = bytes.TrimSpace(body)
	if len(body) > 0 && body[0] == '[' {
		var reqs []request
		if err := json.Unmarshal(body, &reqs); err != nil {
			s.write(w, http.StatusInternalServerError, response{Error: errorf(codeParse, "Parse error")})
			return
		}
		out := make([]response, len(reqs))
		for i, req := range reqs {
			out[i] = s.handle(r.Context(), req)
		}
		s.write(w, http.StatusOK, out)
		return
	}
	var req request
	if err := json.Unmarshal(body, &req); err != nil {
		s.write(w, http.StatusInternalServerError, response{Error: errorf(codeParse, "Parse error")})
		return
	}
	resp := s.handle(r.Context(), req)
	status := http.StatusOK
	if resp.Error != nil {
		// Litecoin Core's status codes for JSON-RPC 1.0 errors.
		switch resp.Error.Code {
		case codeInvalidRequest:
			status = http.StatusBadRequest
		case codeMethodNotFound:
			status = http.StatusNotFound
		default:
			status = http.StatusInternalServerError
		}
	}
	s.write(w, status, resp)
}

func (s *Server) write(w http.ResponseWriter, status int, v any) {
	w.WriteHeader(status)
	if err := json.NewEncoder(w).Encode(v); err != nil && s.Log != nil {
		s.Log.Warn("api: write response", "err", err)
	}
}

func (s *Server) handle(ctx context.Context, req request) response {
	resp := response{ID: req.ID}
	if req.ID == nil {
		resp.ID = json.RawMessage("null")
	}
	result, err := s.call(ctx, req.Method, req.Params)
	if err != nil {
		var e *Error
		var nodeErr *rpc.Error
		switch {
		case errors.As(err, &e):
			resp.Error = e
		case errors.As(err, &nodeErr):
			resp.Error = &Error{Code: nodeErr.Code, Message: nodeErr.Message}
		default:
			if s.Log != nil {
				s.Log.Error("api call failed", "method", req.Method, "err", err)
			}
			resp.Error = errorf(codeInternal, err.Error())
		}
		return resp
	}
	resp.Result = result
	return resp
}

func (s *Server) call(ctx context.Context, method string, params []json.RawMessage) (any, error) {
	if h, ok := methods[method]; ok {
		var result any
		err := s.Store.Read(ctx, func(snap *store.Snapshot) error {
			q := newQuery(snap, s.Params)
			var err error
			result, err = h(q, args(params))
			return err
		})
		return result, err
	}
	if passThrough[method] && s.Node != nil {
		var raw json.RawMessage
		ps := make([]any, len(params))
		for i, p := range params {
			ps[i] = p
		}
		if err := s.Node.Call(ctx, method, &raw, ps...); err != nil {
			return nil, err
		}
		return raw, nil
	}
	if strings.HasPrefix(method, "omni_") {
		return nil, errorf(codeMethodNotFound, "Method not found (not supported by omnilite-standalone)")
	}
	return nil, errorf(codeMethodNotFound, "Method not found")
}
