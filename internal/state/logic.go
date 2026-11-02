package state

import (
	"encoding/json"
	"fmt"
	"math"

	"github.com/tbuuol/omnilite-standalone/internal/chain"
	"github.com/tbuuol/omnilite-standalone/internal/omni"
)

// Error code bases (PKT_ERROR_* in omnicore.h).
const (
	pktError        = -9000
	pktErrorSP      = -40000
	pktErrorCrowd   = -45000
	pktErrorSTO     = -50000
	pktErrorSend    = -60000
	pktErrorOffer   = -70000
	pktErrorTokens  = -82000
	pktErrorSendAll = -83000
	pktErrorAnyData = -84000
	pktErrorNFT     = -85000

	dexErrorSellOffer = -10000
	dexErrorAccept    = -20000
	dexErrorPayment   = -30000
)

// Tx is a structurally valid Omni transaction ready to be applied.
type Tx struct {
	Txid      string
	Block     int64
	Index     int
	BlockHash string
	BlockTime int64
	Sender    string
	Msg       *omni.Message
	// Fee is the miner fee in litoshi; a DEx accept must pay the offer's minimum.
	Fee int64
}

// SendAllItem is one property moved by a Send All.
type SendAllItem struct {
	Property uint32 `json:"property"`
	Amount   int64  `json:"amount"`
}

// Result is what OmniLite records about a transaction: its validity, the
// interpretPacket return code and the "new amount".
type Result struct {
	Valid     bool          `json:"valid"`
	Code      int           `json:"code"`
	Type      uint16        `json:"type"`
	NewAmount uint64        `json:"newamount"`
	SendAll   []SendAllItem `json:"sendall,omitempty"`
	STO       []STOReceive  `json:"sto,omitempty"`
	// GrantStart and GrantEnd are the token range created by a grant of a
	// non-fungible property.
	GrantStart int64 `json:"grantstart,omitempty"`
	GrantEnd   int64 `json:"grantend,omitempty"`
	// Payments lists the DEx purchases of a payment transaction.
	Payments []Purchase `json:"payments,omitempty"`
}

// Apply runs the logic of one transaction (interpretPacket after the
// structural check) and records the result.
func (s *State) Apply(t *Tx) (*Result, error) {
	m := t.Msg
	res := &Result{Type: m.Type, NewAmount: m.Amount}
	code, err := s.apply(t, res)
	if err != nil {
		return nil, fmt.Errorf("tx %s: %w", t.Txid, err)
	}
	res.Code, res.Valid = code, code == 0
	v, err := json.Marshal(res)
	if err != nil {
		return nil, err
	}
	return res, s.tx.Put(NsResult, t.Txid, v)
}

func (s *State) apply(t *Tx, res *Result) (int, error) {
	m := t.Msg
	if frozen, err := s.frozen(t.Sender, m.Property); err != nil || frozen {
		return pktError - 3, err
	}
	switch m.Type {
	case omni.TypeSimpleSend:
		return s.simpleSend(t)
	case omni.TypeSendAll:
		return s.sendAll(t, res)
	case omni.TypeCreatePropertyFixed:
		return s.createFixed(t)
	case omni.TypeCreatePropertyMan:
		return s.createManaged(t)
	case omni.TypeGrantTokens:
		return s.grant(t, res)
	case omni.TypeRevokeTokens:
		return s.revoke(t)
	case omni.TypeChangeIssuer:
		return s.changeIssuer(t)
	case omni.TypeAnyData:
		return s.anyData(t)
	case omni.TypeActivation:
		return s.activation(t)
	case omni.TypeDeactivation:
		return s.deactivation(t)
	case omni.TypeAlert:
		return s.alert(t)
	case omni.TypeSendToOwners:
		return s.sendToOwners(t, res)
	case omni.TypeSendNonFungible:
		return s.sendNonFungible(t)
	case omni.TypeTradeOffer:
		return s.tradeOffer(t)
	case omni.TypeAcceptOffer:
		return s.acceptOffer(t, res)
	case omni.TypeCreatePropertyVar:
		return s.createCrowdsale(t)
	case omni.TypeCloseCrowdsale:
		return s.closeCrowdsale(t)
	case omni.TypeEnableFreezing:
		return s.enableFreezing(t)
	case omni.TypeDisableFreezing:
		return s.disableFreezingTx(t)
	case omni.TypeFreezeTokens:
		return s.freeze(t, true)
	case omni.TypeUnfreezeTokens:
		return s.freeze(t, false)
	case omni.TypeNonFungibleData:
		return s.nonFungibleData(t)
	}
	// interpret_Transaction only lets the types above through.
	return 0, fmt.Errorf("no logic for type %d", m.Type)
}

func validAmount(v uint64) bool { return v > 0 && v <= math.MaxInt64 }

// logicMath_SimpleSend.
func (s *State) simpleSend(t *Tx) (int, error) {
	m := t.Msg
	if ok, err := s.typeAllowed(t.Block, m.Property, m.Type, m.Version); err != nil || !ok {
		return pktErrorSend - 22, err
	}
	if nft, err := s.nonFungible(m.Property); err != nil || nft {
		return pktErrorNFT - 21, err
	}
	if !validAmount(m.Amount) {
		return pktErrorSend - 23, nil
	}
	if ok, err := s.validPropertyID(m.Property); err != nil || !ok {
		return pktErrorSend - 24, err
	}
	amount := int64(m.Amount)
	bal, err := s.Balance(t.Sender, m.Property, Balance)
	if err != nil || bal < amount {
		return pktErrorSend - 25, err
	}
	if err := s.mustUpdate(t.Sender, m.Property, -amount, Balance); err != nil {
		return 0, err
	}
	if err := s.mustUpdate(m.Receiver, m.Property, amount, Balance); err != nil {
		return 0, err
	}
	return 0, s.crowdsaleParticipation(t)
}

// logicMath_SendAll. The ecosystem byte doubles as property for the checks.
func (s *State) sendAll(t *Tx, res *Result) (int, error) {
	m := t.Msg
	if ok, err := s.typeAllowed(t.Block, uint32(m.Ecosystem), m.Type, m.Version); err != nil || !ok {
		return pktErrorSendAll - 22, err
	}
	issuerData, err := s.FeatureActive(chain.FeatureNonFungibleIssuer, t.Block)
	if err != nil {
		return 0, err
	}
	if !issuerData {
		if nft, err := s.nonFungible(m.Property); err != nil || nft {
			return pktErrorNFT - 21, err
		}
	}
	if ok, err := s.hasTally(t.Sender); err != nil || !ok {
		return pktErrorSendAll - 54, err
	}

	// The tally map iterates properties in ascending order, as the
	// zero-padded keys do.
	kvs, err := s.tx.Scan(NsBalance, t.Sender+"/")
	if err != nil {
		return 0, err
	}
	sent := 0
	for _, kv := range kvs {
		var id uint32
		if _, err := fmt.Sscanf(kv.Key[len(t.Sender)+1:], "%d", &id); err != nil {
			return 0, err
		}
		if m.Ecosystem == 1 && isTestEcosystem(id) {
			continue
		}
		if m.Ecosystem == 2 && isMainEcosystem(id) {
			continue
		}
		if frozen, err := s.frozen(t.Sender, id); err != nil {
			return 0, err
		} else if frozen {
			continue
		}
		if issuerData {
			if nft, err := s.nonFungible(id); err != nil {
				return 0, err
			} else if nft {
				continue
			}
		}
		avail, err := s.Balance(t.Sender, id, Balance)
		if err != nil {
			return 0, err
		}
		if avail <= 0 {
			continue
		}
		sent++
		if err := s.mustUpdate(t.Sender, id, -avail, Balance); err != nil {
			return 0, err
		}
		if err := s.mustUpdate(m.Receiver, id, avail, Balance); err != nil {
			return 0, err
		}
		res.SendAll = append(res.SendAll, SendAllItem{Property: id, Amount: avail})
	}
	if sent == 0 {
		return pktErrorSendAll - 55, nil
	}
	res.NewAmount = uint64(sent)
	return 0, nil
}

func newProperty(t *Tx) *Property {
	m := t.Msg
	p := &Property{
		Issuer: t.Sender, Txid: t.Txid, PropType: m.PropType, PrevPropID: m.PrevPropID,
		Category: []byte(m.Category), Subcategory: []byte(m.Subcategory), Name: []byte(m.Name),
		URL: []byte(m.URL), Data: []byte(m.Data),
		CreationBlock: t.BlockHash, UpdateBlock: t.BlockHash,
	}
	p.updateIssuer(t.Block, t.Index, t.Sender)
	return p
}

// logicMath_CreatePropertyFixed.
func (s *State) createFixed(t *Tx) (int, error) {
	m := t.Msg
	if m.Ecosystem != 1 && m.Ecosystem != 2 {
		return pktErrorSP - 21, nil
	}
	if ok, err := s.typeAllowed(t.Block, uint32(m.Ecosystem), m.Type, m.Version); err != nil || !ok {
		return pktErrorSP - 22, err
	}
	if !validAmount(m.Amount) {
		return pktErrorSP - 23, nil
	}
	if m.PropType != PropTypeIndivisible && m.PropType != PropTypeDivisible {
		return pktErrorSP - 36, nil
	}
	if m.Name == "" {
		return pktErrorSP - 37, nil
	}
	p := newProperty(t)
	p.NumTokens = int64(m.Amount)
	p.Fixed = true
	id, err := s.putNewProperty(m.Ecosystem, p)
	if err != nil {
		return 0, err
	}
	return 0, s.mustUpdate(t.Sender, id, int64(m.Amount), Balance)
}

// logicMath_CreatePropertyManaged.
func (s *State) createManaged(t *Tx) (int, error) {
	m := t.Msg
	if m.Ecosystem != 1 && m.Ecosystem != 2 {
		return pktErrorSP - 21, nil
	}
	if ok, err := s.typeAllowed(t.Block, uint32(m.Ecosystem), m.Type, m.Version); err != nil || !ok {
		return pktErrorSP - 22, err
	}
	if m.PropType != PropTypeIndivisible && m.PropType != PropTypeDivisible && m.PropType != PropTypeNonFungible {
		return pktErrorSP - 36, nil
	}
	if m.PropType == PropTypeNonFungible {
		if active, err := s.FeatureActive(chain.FeatureNonFungible, t.Block); err != nil || !active {
			return pktErrorNFT - 20, err
		}
	}
	if m.Name == "" {
		return pktErrorSP - 37, nil
	}
	p := newProperty(t)
	p.Unique = m.PropType == PropTypeNonFungible
	p.Manual = true
	_, err := s.putNewProperty(m.Ecosystem, p)
	return 0, err
}

// property loads a property that validPropertyID already accepted.
func (s *State) property(id uint32) (*Property, error) {
	p, err := s.Property(id)
	if err == nil && p == nil {
		err = fmt.Errorf("property %d is valid but missing", id)
	}
	return p, err
}

// logicMath_GrantTokens.
func (s *State) grant(t *Tx, res *Result) (int, error) {
	m := t.Msg
	if ok, err := s.typeAllowed(t.Block, m.Property, m.Type, m.Version); err != nil || !ok {
		return pktErrorTokens - 22, err
	}
	if !validAmount(m.Amount) {
		return pktErrorTokens - 23, nil
	}
	if ok, err := s.validPropertyID(m.Property); err != nil || !ok {
		return pktErrorTokens - 24, err
	}
	p, err := s.property(m.Property)
	if err != nil {
		return 0, err
	}
	if !p.Manual {
		return pktErrorTokens - 42, nil
	}
	if t.Sender != p.IssuerAt(t.Block) {
		return pktErrorTokens - 43, nil
	}
	total, err := s.TotalTokens(m.Property)
	if err != nil {
		return 0, err
	}
	amount := int64(m.Amount)
	if amount > math.MaxInt64-total {
		return pktErrorTokens - 44, nil
	}
	p.History = insertHistory(p.History, HistoryEntry{Txid: t.Txid, Data: []int64{amount, 0}})
	p.UpdateBlock = t.BlockHash
	if err := s.putProperty(p); err != nil {
		return 0, err
	}
	if p.Unique {
		if res.GrantStart, res.GrantEnd, err = s.nftCreate(m.Property, amount, m.Receiver, m.NFTData); err != nil {
			return 0, err
		}
	}
	return 0, s.mustUpdate(m.Receiver, m.Property, amount, Balance)
}

// logicMath_RevokeTokens.
func (s *State) revoke(t *Tx) (int, error) {
	m := t.Msg
	if ok, err := s.typeAllowed(t.Block, m.Property, m.Type, m.Version); err != nil || !ok {
		return pktErrorTokens - 22, err
	}
	if nft, err := s.nonFungible(m.Property); err != nil || nft {
		return pktErrorNFT - 21, err
	}
	if !validAmount(m.Amount) {
		return pktErrorTokens - 23, nil
	}
	if ok, err := s.validPropertyID(m.Property); err != nil || !ok {
		return pktErrorTokens - 24, err
	}
	p, err := s.property(m.Property)
	if err != nil {
		return 0, err
	}
	if !p.Manual {
		return pktErrorTokens - 42, nil
	}
	amount := int64(m.Amount)
	bal, err := s.Balance(t.Sender, m.Property, Balance)
	if err != nil || bal < amount {
		return pktErrorTokens - 25, err
	}
	p.History = insertHistory(p.History, HistoryEntry{Txid: t.Txid, Data: []int64{0, amount}})
	p.UpdateBlock = t.BlockHash
	if err := s.mustUpdate(t.Sender, m.Property, -amount, Balance); err != nil {
		return 0, err
	}
	return 0, s.putProperty(p)
}

// logicMath_ChangeIssuer.
func (s *State) changeIssuer(t *Tx) (int, error) {
	m := t.Msg
	if ok, err := s.typeAllowed(t.Block, m.Property, m.Type, m.Version); err != nil || !ok {
		return pktErrorTokens - 22, err
	}
	if nft, err := s.nonFungible(m.Property); err != nil || nft {
		return pktErrorNFT - 21, err
	}
	if ok, err := s.validPropertyID(m.Property); err != nil || !ok {
		return pktErrorTokens - 24, err
	}
	p, err := s.property(m.Property)
	if err != nil {
		return 0, err
	}
	if t.Sender != p.IssuerAt(t.Block) {
		return pktErrorTokens - 43, nil
	}
	if c, err := s.Crowdsale(t.Sender); err != nil || c != nil {
		return pktErrorTokens - 39, err
	}
	if m.Receiver == "" {
		return pktErrorTokens - 45, nil
	}
	if c, err := s.Crowdsale(m.Receiver); err != nil || c != nil {
		return pktErrorTokens - 46, err
	}
	if m.Property == PropertyOmni || m.Property == PropertyTestOmni {
		// updateSP on an implied property writes an entry getSP never
		// reads back, so nothing changes.
		return 0, nil
	}
	p.updateIssuer(t.Block, t.Index, m.Receiver)
	p.Issuer = m.Receiver
	p.UpdateBlock = t.BlockHash
	return 0, s.putProperty(p)
}

// logicMath_AnyData.
func (s *State) anyData(t *Tx) (int, error) {
	if ok, err := s.typeAllowed(t.Block, t.Msg.Property, t.Msg.Type, t.Msg.Version); err != nil || !ok {
		return pktErrorAnyData - 22, err
	}
	return 0, nil
}

func authorized(sender string, extra map[string]bool) bool {
	return sender == chain.Authority || extra[sender] || extra["any"]
}

func alertKey(t *Tx) string { return fmt.Sprintf("%010d:%05d", t.Block, t.Index) }

// logicMath_Deactivation.
func (s *State) deactivation(t *Tx) (int, error) {
	m := t.Msg
	if ok, err := s.typeAllowed(t.Block, m.Property, m.Type, m.Version); err != nil || !ok {
		return pktError - 22, err
	}
	if !authorized(t.Sender, s.activationAllow) {
		return pktError - 51, nil
	}
	if ok, err := s.deactivate(m.FeatureID, t.Block, alertKey(t)); err != nil || !ok {
		return pktError - 54, err
	}
	return 0, nil
}

// logicMath_Activation.
func (s *State) activation(t *Tx) (int, error) {
	m := t.Msg
	if ok, err := s.typeAllowed(t.Block, m.Property, m.Type, m.Version); err != nil || !ok {
		return pktError - 22, err
	}
	if !authorized(t.Sender, s.activationAllow) {
		return pktError - 51, nil
	}
	if ok, err := s.activate(m.FeatureID, int64(m.ActivationBlock), m.MinClientVersion, t.Block, alertKey(t)); err != nil || !ok {
		return pktError - 54, err
	}
	return 0, nil
}

// logicMath_Alert. OmniLite shuts down on a client-version alert outside
// mainnet; that is not consensus and left out here.
func (s *State) alert(t *Tx) (int, error) {
	m := t.Msg
	if ok, err := s.typeAllowed(t.Block, m.Property, m.Type, m.Version); err != nil || !ok {
		return pktError - 22, err
	}
	if !authorized(t.Sender, s.alertAllow) {
		return pktError - 51, nil
	}
	if m.AlertType == 0xFFFF {
		return 0, s.deleteAlerts(t.Sender)
	}
	return 0, s.addAlert(alertKey(t), t.Sender, m.AlertType, m.AlertExpiry, m.AlertText)
}
