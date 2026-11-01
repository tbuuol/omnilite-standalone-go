// Package chain holds the per-network parameters of OmniLite.
package chain

import "fmt"

// Params describes one network. Values are taken from OmniLite 0.18.1
// (src/omnicore/rules.cpp, src/omnicore/omnicore.cpp and src/chainparams.cpp).
type Params struct {
	// Name is the network name used in config files and on the command line.
	Name string
	// CoreChain is the value litecoind reports as "chain" in getblockchaininfo.
	CoreChain string
	// OmniGenesis is the first block in which Omni transactions are parsed
	// (GENESIS_BLOCK in rules.cpp).
	OmniGenesis int64
	// RPCPort is the default litecoind RPC port.
	RPCPort int

	// PubKeyPrefix is the base58 version byte of P2PKH addresses (PUBKEY_ADDRESS).
	PubKeyPrefix byte
	// ScriptPrefix is the legacy P2SH version byte (SCRIPT_ADDRESS, "3..." on
	// mainnet). Litecoin no longer encodes addresses with it, but OmniLite
	// still accepts it in freeze/unfreeze payloads.
	ScriptPrefix byte
	// ScriptPrefix2 is the P2SH version byte addresses are encoded with
	// (SCRIPT_ADDRESS2, "M..." on mainnet).
	ScriptPrefix2 byte
	// Exodus is the Exodus address that marks Class A and B transactions.
	Exodus string
}

var (
	Main = Params{
		Name: "main", CoreChain: "main", OmniGenesis: 2010500, RPCPort: 9332,
		PubKeyPrefix: 48, ScriptPrefix: 5, ScriptPrefix2: 50,
		Exodus: "LTceXoduS2cetpWJSe47M25i5oKjEccN1h",
	}
	Testnet = Params{
		Name: "test", CoreChain: "test", OmniGenesis: 101, RPCPort: 19332,
		PubKeyPrefix: 111, ScriptPrefix: 196, ScriptPrefix2: 58,
		Exodus: "QeVAq4mudsSSvyM6mBkd7cUuPsgBBrLq83",
	}
	Regtest = Params{
		Name: "regtest", CoreChain: "regtest", OmniGenesis: 101, RPCPort: 19443,
		PubKeyPrefix: 111, ScriptPrefix: 196, ScriptPrefix2: 58,
		Exodus: "QMSH3Dp8raecReSw7hBbbCCfDpUGXQUz1m",
	}
)

// ByName returns the parameters for a network name.
func ByName(name string) (Params, error) {
	switch name {
	case "main", "mainnet", "":
		return Main, nil
	case "test", "testnet", "testnet4":
		return Testnet, nil
	case "regtest":
		return Regtest, nil
	}
	return Params{}, fmt.Errorf("unknown network %q (use main, test or regtest)", name)
}
