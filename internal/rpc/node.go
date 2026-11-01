package rpc

import (
	"context"
	"fmt"
)

type BlockchainInfo struct {
	Chain                string  `json:"chain"`
	Blocks               int64   `json:"blocks"`
	Headers              int64   `json:"headers"`
	BestBlockHash        string  `json:"bestblockhash"`
	InitialBlockDownload bool    `json:"initialblockdownload"`
	VerificationProgress float64 `json:"verificationprogress"`
	Pruned               bool    `json:"pruned"`
}

type NetworkInfo struct {
	Version    int    `json:"version"`
	Subversion string `json:"subversion"`
}

type BlockHeader struct {
	Hash              string `json:"hash"`
	Height            int64  `json:"height"`
	PreviousBlockHash string `json:"previousblockhash"`
	Time              int64  `json:"time"`
}

func (c *Client) GetBlockchainInfo(ctx context.Context) (*BlockchainInfo, error) {
	var r BlockchainInfo
	return &r, c.Call(ctx, "getblockchaininfo", &r)
}

func (c *Client) GetNetworkInfo(ctx context.Context) (*NetworkInfo, error) {
	var r NetworkInfo
	return &r, c.Call(ctx, "getnetworkinfo", &r)
}

func (c *Client) GetBlockCount(ctx context.Context) (int64, error) {
	var r int64
	return r, c.Call(ctx, "getblockcount", &r)
}

func (c *Client) GetBlockHash(ctx context.Context, height int64) (string, error) {
	var r string
	return r, c.Call(ctx, "getblockhash", &r, height)
}

func (c *Client) GetBlockHeader(ctx context.Context, hash string) (*BlockHeader, error) {
	var r BlockHeader
	return &r, c.Call(ctx, "getblockheader", &r, hash, true)
}

// TxIndex reports whether the node runs with -txindex and whether that
// index has caught up. Without it, input prevouts cannot be looked up.
func (c *Client) TxIndex(ctx context.Context) (enabled, synced bool, err error) {
	var info map[string]struct {
		Synced bool `json:"synced"`
	}
	err = c.Call(ctx, "getindexinfo", &info)
	if err == nil {
		ti, ok := info["txindex"]
		return ok, ok && ti.Synced, nil
	}
	if !IsCode(err, CodeMethodNotFound) {
		return false, false, err
	}

	// Nodes before 0.21 have no getindexinfo: probe with the coinbase of
	// block 1, which is only reachable through the transaction index.
	hash, err := c.GetBlockHash(ctx, 1)
	if err != nil {
		return false, false, err
	}
	var block struct {
		Tx []string `json:"tx"`
	}
	if err := c.Call(ctx, "getblock", &block, hash, 1); err != nil {
		return false, false, err
	}
	if len(block.Tx) == 0 {
		return false, false, fmt.Errorf("block 1 has no transactions")
	}
	err = c.Call(ctx, "getrawtransaction", nil, block.Tx[0], false)
	if IsCode(err, CodeInvalidAddressKey) {
		return false, false, nil
	}
	if err != nil {
		return false, false, err
	}
	// Synced state is unknown here; old nodes build the index before serving RPC.
	return true, true, nil
}
