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

	// Features holds the initial activation block of each feature ID
	// (CConsensusParams). Activation transactions change these at runtime.
	Features map[uint16]int64
	// Min- and MaxActivationBlocks bound how far ahead an activation may be
	// scheduled (MIN_/MAX_ACTIVATION_BLOCKS).
	MinActivationBlocks, MaxActivationBlocks int64
}

// Feature IDs (src/omnicore/rules.h).
const (
	FeatureSTOv1             uint16 = 10
	FeatureFreezeNotice      uint16 = 14
	FeatureFreeDEx           uint16 = 15
	FeatureNonFungible       uint16 = 16
	FeatureNonFungibleIssuer uint16 = 18
)

// Authority is the only address allowed to send activations, deactivations
// and alerts, on every network (activation.cpp, notifications.cpp).
const Authority = "MJDAXHQMxdRfKS2b3hECsSCf4iyx1G8CCT"

// NotActive is the block height OmniLite uses for "never".
const NotActive = 100000000

var (
	Main = Params{
		Name: "main", CoreChain: "main", OmniGenesis: 2010500, RPCPort: 9332,
		PubKeyPrefix: 48, ScriptPrefix: 5, ScriptPrefix2: 50,
		Exodus: "LTceXoduS2cetpWJSe47M25i5oKjEccN1h",
		Features: map[uint16]int64{
			FeatureSTOv1: NotActive, FeatureFreezeNotice: NotActive, FeatureFreeDEx: 2010500,
			FeatureNonFungible: 2262000, FeatureNonFungibleIssuer: NotActive,
		},
		MinActivationBlocks: 8192, MaxActivationBlocks: 49152,
	}
	Testnet = Params{
		Name: "test", CoreChain: "test", OmniGenesis: 101, RPCPort: 19332,
		PubKeyPrefix: 111, ScriptPrefix: 196, ScriptPrefix2: 58,
		Exodus: "QeVAq4mudsSSvyM6mBkd7cUuPsgBBrLq83",
		Features: map[uint16]int64{
			FeatureSTOv1: 0, FeatureFreezeNotice: 0, FeatureFreeDEx: 0, FeatureNonFungible: 0, FeatureNonFungibleIssuer: 0,
		},
		MinActivationBlocks: 0, MaxActivationBlocks: 9999999,
	}
	Regtest = Params{
		Name: "regtest", CoreChain: "regtest", OmniGenesis: 101, RPCPort: 19443,
		PubKeyPrefix: 111, ScriptPrefix: 196, ScriptPrefix2: 58,
		Exodus: "QMSH3Dp8raecReSw7hBbbCCfDpUGXQUz1m",
		Features: map[uint16]int64{
			FeatureSTOv1: NotActive, FeatureFreezeNotice: NotActive, FeatureFreeDEx: NotActive,
			FeatureNonFungible: 0, FeatureNonFungibleIssuer: NotActive,
		},
		MinActivationBlocks: 5, MaxActivationBlocks: 10,
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
