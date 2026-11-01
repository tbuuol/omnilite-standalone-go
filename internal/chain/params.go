// Package chain holds the per-network parameters of OmniLite.
package chain

import "fmt"

// Params describes one network. Values are taken from OmniLite 0.18.1
// (src/omnicore/rules.cpp and src/chainparamsbase.cpp).
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
}

var (
	Main    = Params{Name: "main", CoreChain: "main", OmniGenesis: 2010500, RPCPort: 9332}
	Testnet = Params{Name: "test", CoreChain: "test", OmniGenesis: 101, RPCPort: 19332}
	Regtest = Params{Name: "regtest", CoreChain: "regtest", OmniGenesis: 101, RPCPort: 19443}
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
