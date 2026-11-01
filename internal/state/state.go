// Package state holds the Omni state (balances, properties, feature
// activations, alerts) and applies transactions to it like OmniLite 0.18.1
// (src/omnicore/tx.cpp logicMath_*, tally.cpp, dbspinfo.cpp, rules.cpp).
//
// All reads and writes go through the block's store transaction, so a
// disconnected block takes its state changes with it.
package state

import (
	"encoding/binary"
	"encoding/json"
	"fmt"
	"math"
	"strconv"

	"github.com/tbuuol/omnilite-standalone/internal/chain"
	"github.com/tbuuol/omnilite-standalone/internal/store"
)

// Store namespaces.
const (
	NsBalance    = "bal"        // address/property → balance, sell-offer and accept reserve
	NsTally      = "tally"      // address → present once the address had any tally update
	NsProperty   = "prop"       // property → Property (JSON)
	NsPropertyTx = "proptx"     // creation txid → property
	NsTotal      = "total"      // property → tokens held in balances and reserves
	NsMeta       = "meta"       // next property IDs, incomplete marker
	NsFeature    = "feature"    // feature → activation block, once changed by a transaction
	NsActivation = "activation" // pending and completed activations
	NsAlert      = "alert"      // active alerts
	NsFrozen     = "frozen"     // address/property → frozen (written from phase 4)
	NsResult     = "result"     // txid → Result (JSON)
)

// Property IDs and limits (src/omnicore/omnicore.h).
const (
	PropertyLTC      uint32 = 0
	PropertyOmni     uint32 = 1
	PropertyTestOmni uint32 = 2
	TestEcoFirst     uint32 = 0x80000003

	maxInt8Bytes = math.MaxInt64
)

// Property types.
const (
	PropTypeIndivisible uint16 = 1
	PropTypeDivisible   uint16 = 2
	PropTypeNonFungible uint16 = 5
)

// OmniCoreVersion is OMNICORE_VERSION of OmniLite 0.18.1 (0.9.1.0). It
// decides whether an activation is supported.
const OmniCoreVersion = 90001000

// Tally types (TallyType in tally.h). PENDING is mempool state and not kept here.
const (
	Balance = iota
	SellOfferReserve
	AcceptReserve
	tallyTypes
)

// IssuerChange is one entry of historicalIssuers.
type IssuerChange struct {
	Block  int64  `json:"block"`
	Index  int    `json:"index"`
	Issuer string `json:"issuer"`
}

// TokenChange is one entry of historicalData: tokens granted or revoked by
// a transaction of a managed property.
type TokenChange struct {
	Txid    string `json:"txid"`
	Granted int64  `json:"granted"`
	Revoked int64  `json:"revoked"`
}

// Property is a smart property (CMPSPInfo::Entry). Strings are kept as raw
// bytes because payloads need not be valid UTF-8.
type Property struct {
	ID            uint32         `json:"id"`
	Issuer        string         `json:"issuer"`
	Txid          string         `json:"txid"`
	PropType      uint16         `json:"type"`
	PrevPropID    uint32         `json:"prev"`
	NumTokens     int64          `json:"tokens"`
	Category      []byte         `json:"category"`
	Subcategory   []byte         `json:"subcategory"`
	Name          []byte         `json:"name"`
	URL           []byte         `json:"url"`
	Data          []byte         `json:"data"`
	Fixed         bool           `json:"fixed"`
	Manual        bool           `json:"manual"`
	Unique        bool           `json:"unique"`
	CreationBlock string         `json:"creationblock"`
	UpdateBlock   string         `json:"updateblock"`
	Issuers       []IssuerChange `json:"issuers"`
	History       []TokenChange  `json:"history,omitempty"`
}

// Divisible mirrors Entry::isDivisible.
func (p *Property) Divisible() bool {
	switch p.PropType {
	case 2, 66, 130:
		return true
	}
	return false
}

// IssuerAt mirrors Entry::getIssuer: the last change in a block at or below
// block. Changes later in the same block count as well.
func (p *Property) IssuerAt(block int64) string {
	issuer := p.Issuer
	for _, c := range p.Issuers {
		if c.Block > block {
			break
		}
		issuer = c.Issuer
	}
	return issuer
}

// updateIssuer mirrors Entry::updateIssuer on the (block, index) ordered map.
func (p *Property) updateIssuer(block int64, index int, issuer string) {
	for i, c := range p.Issuers {
		if c.Block == block && c.Index == index {
			p.Issuers[i].Issuer = issuer
			return
		}
		if c.Block > block || (c.Block == block && c.Index > index) {
			p.Issuers = append(p.Issuers[:i], append([]IssuerChange{{block, index, issuer}}, p.Issuers[i:]...)...)
			return
		}
	}
	p.Issuers = append(p.Issuers, IssuerChange{block, index, issuer})
}

// State works on one block's store transaction.
type State struct {
	tx     *store.BlockTx
	params *chain.Params
	// Extra authorized senders, like -omniactivationallowsender and
	// -omnialertallowsender. They change consensus and are for tests only.
	activationAllow map[string]bool
	alertAllow      map[string]bool
}

// Options are the testing switches of OmniLite that affect the state.
type Options struct {
	ActivationAllowSenders []string
	AlertAllowSenders      []string
}

func New(tx *store.BlockTx, params *chain.Params, opt Options) *State {
	s := &State{tx: tx, params: params, activationAllow: map[string]bool{}, alertAllow: map[string]bool{}}
	for _, a := range opt.ActivationAllowSenders {
		s.activationAllow[a] = true
	}
	for _, a := range opt.AlertAllowSenders {
		s.alertAllow[a] = true
	}
	return s
}

func propKey(id uint32) string { return fmt.Sprintf("%010d", id) }

func balanceKey(addr string, id uint32) string { return addr + "/" + propKey(id) }

func isTestEcosystem(id uint32) bool { return id == PropertyTestOmni || id >= TestEcoFirst }

func isMainEcosystem(id uint32) bool { return id != PropertyLTC && !isTestEcosystem(id) }

// --- tallies ---

func (s *State) tally(addr string, id uint32) ([tallyTypes]int64, error) {
	var t [tallyTypes]int64
	v, ok, err := s.tx.Get(NsBalance, balanceKey(addr, id))
	if err != nil || !ok {
		return t, err
	}
	if len(v) != 8*tallyTypes {
		return t, fmt.Errorf("corrupt balance %s", balanceKey(addr, id))
	}
	for i := range t {
		t[i] = int64(binary.BigEndian.Uint64(v[8*i:]))
	}
	return t, nil
}

// Balance returns one tally type of an address.
func (s *State) Balance(addr string, id uint32, ttype int) (int64, error) {
	t, err := s.tally(addr, id)
	return t[ttype], err
}

func (s *State) hasTally(addr string) (bool, error) {
	_, ok, err := s.tx.Get(NsTally, addr)
	return ok, err
}

// updateTally mirrors update_tally_map. It reports false where OmniLite's
// updateMoney refuses (overflow or a negative result).
func (s *State) updateTally(addr string, id uint32, amount int64, ttype int) (bool, error) {
	if amount == 0 {
		return false, nil
	}
	if ok, err := s.hasTally(addr); err != nil {
		return false, err
	} else if !ok {
		if err := s.tx.Put(NsTally, addr, nil); err != nil {
			return false, err
		}
	}
	t, err := s.tally(addr, id)
	if err != nil {
		return false, err
	}
	now := t[ttype]
	if (amount > 0 && now > math.MaxInt64-amount) || (amount < 0 && now < math.MinInt64-amount) || now+amount < 0 {
		return false, nil
	}
	t[ttype] = now + amount
	v := make([]byte, 8*tallyTypes)
	for i := range t {
		binary.BigEndian.PutUint64(v[8*i:], uint64(t[i]))
	}
	if err := s.tx.Put(NsBalance, balanceKey(addr, id), v); err != nil {
		return false, err
	}
	total, err := s.total(id)
	if err != nil {
		return false, err
	}
	return true, s.tx.Put(NsTotal, propKey(id), strconv.AppendInt(nil, total+amount, 10))
}

// mustUpdate is update_tally_map wrapped in assert(): a refusal means the
// state is broken.
func (s *State) mustUpdate(addr string, id uint32, amount int64, ttype int) error {
	ok, err := s.updateTally(addr, id, amount, ttype)
	if err != nil {
		return err
	}
	if !ok {
		return fmt.Errorf("tally update refused: %s property %d amount %d type %d", addr, id, amount, ttype)
	}
	return nil
}

func (s *State) total(id uint32) (int64, error) {
	v, ok, err := s.tx.Get(NsTotal, propKey(id))
	if err != nil || !ok {
		return 0, err
	}
	return strconv.ParseInt(string(v), 10, 64)
}

// TotalTokens mirrors getTotalTokens: the fixed amount for fixed
// properties, otherwise all balances and reserves.
func (s *State) TotalTokens(id uint32) (int64, error) {
	p, err := s.Property(id)
	if err != nil || p == nil {
		return 0, err
	}
	if p.Fixed {
		return p.NumTokens, nil
	}
	return s.total(id)
}

// --- properties ---

func (s *State) impliedProperty(id uint32) *Property {
	return &Property{
		ID: id, Issuer: s.params.Exodus, PropType: PropTypeDivisible,
		Category: []byte("N/A"), Subcategory: []byte("N/A"), Name: []byte("Reserved"),
		URL: []byte("N/A"), Data: []byte("Reserved"),
		Issuers: []IssuerChange{{0, 0, s.params.Exodus}},
	}
}

// Property returns a property, or nil if it does not exist.
func (s *State) Property(id uint32) (*Property, error) {
	if id == PropertyOmni || id == PropertyTestOmni {
		return s.impliedProperty(id), nil
	}
	v, ok, err := s.tx.Get(NsProperty, propKey(id))
	if err != nil || !ok {
		return nil, err
	}
	var p Property
	return &p, json.Unmarshal(v, &p)
}

func (s *State) putProperty(p *Property) error {
	v, err := json.Marshal(p)
	if err != nil {
		return err
	}
	return s.tx.Put(NsProperty, propKey(p.ID), v)
}

func (s *State) nextID(ecosystem uint8) (uint32, error) {
	def := uint32(3)
	if ecosystem == 2 {
		def = TestEcoFirst
	}
	v, ok, err := s.tx.Get(NsMeta, "next/"+strconv.Itoa(int(ecosystem)))
	if err != nil || !ok {
		return def, err
	}
	n, err := strconv.ParseUint(string(v), 10, 32)
	return uint32(n), err
}

// putNewProperty mirrors putSP: it assigns the next ID of the ecosystem.
func (s *State) putNewProperty(ecosystem uint8, p *Property) (uint32, error) {
	id, err := s.nextID(ecosystem)
	if err != nil {
		return 0, err
	}
	if err := s.tx.Put(NsMeta, "next/"+strconv.Itoa(int(ecosystem)), strconv.AppendUint(nil, uint64(id)+1, 10)); err != nil {
		return 0, err
	}
	p.ID = id
	if err := s.putProperty(p); err != nil {
		return 0, err
	}
	return id, s.tx.Put(NsPropertyTx, p.Txid, strconv.AppendUint(nil, uint64(id), 10))
}

// validPropertyID mirrors IsPropertyIdValid.
func (s *State) validPropertyID(id uint32) (bool, error) {
	if id == 0 {
		return false, nil
	}
	eco := uint8(1)
	if id >= TestEcoFirst {
		eco = 2
	}
	next, err := s.nextID(eco)
	return id < next, err
}

// nonFungible mirrors isPropertyNonFungible.
func (s *State) nonFungible(id uint32) (bool, error) {
	p, err := s.Property(id)
	return p != nil && p.Unique, err
}

func (s *State) frozen(addr string, id uint32) (bool, error) {
	_, ok, err := s.tx.Get(NsFrozen, balanceKey(addr, id))
	return ok, err
}
