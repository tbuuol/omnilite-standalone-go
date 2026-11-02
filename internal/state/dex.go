package state

import (
	"encoding/json"
	"fmt"
	"math/big"
	"strconv"
	"strings"

	"github.com/tbuuol/omnilite-standalone/internal/chain"
	"github.com/tbuuol/omnilite-standalone/internal/omni"
)

// Offer is a DEx sell offer (CMPOffer).
type Offer struct {
	Block          int64  `json:"block"`
	AmountOriginal int64  `json:"amount"`
	Property       uint32 `json:"property"`
	LTCDesired     int64  `json:"desired"`
	MinFee         int64  `json:"minfee"`
	BlockTimeLimit uint8  `json:"timelimit"`
	Txid           string `json:"txid"`
}

// Accept is a DEx accept (CMPAccept).
type Accept struct {
	AmountOriginal      int64  `json:"amount"`
	AmountRemaining     int64  `json:"remaining"`
	BlockTimeLimit      uint8  `json:"timelimit"`
	Property            uint32 `json:"property"`
	OfferAmountOriginal int64  `json:"offeramount"`
	LTCDesiredOriginal  int64  `json:"desired"`
	OfferTxid           string `json:"offertxid"`
	Block               int64  `json:"block"`
}

// Purchase is one DEx payment that bought tokens.
type Purchase struct {
	Vout     int    `json:"vout"`
	Seller   string `json:"seller"`
	Property uint32 `json:"property"`
	Amount   int64  `json:"amount"`
}

func offerKey(seller string, id uint32) string { return fmt.Sprintf("%s-%d", seller, id) }

func acceptKey(seller, buyer string, id uint32) string {
	return fmt.Sprintf("%s-%d+%s", seller, id, buyer)
}

func (s *State) getJSON(ns, key string, v any) (bool, error) {
	b, ok, err := s.tx.Get(ns, key)
	if err != nil || !ok {
		return false, err
	}
	return true, json.Unmarshal(b, v)
}

func (s *State) putJSON(ns, key string, v any) error {
	b, err := json.Marshal(v)
	if err != nil {
		return err
	}
	return s.tx.Put(ns, key, b)
}

// Offer returns the sell offer of a seller for a property, or nil.
func (s *State) Offer(seller string, id uint32) (*Offer, error) {
	var o Offer
	ok, err := s.getJSON(NsOffer, offerKey(seller, id), &o)
	if err != nil || !ok {
		return nil, err
	}
	return &o, nil
}

func (s *State) accept(seller, buyer string, id uint32) (*Accept, error) {
	var a Accept
	ok, err := s.getJSON(NsAccept, acceptKey(seller, buyer, id), &a)
	if err != nil || !ok {
		return nil, err
	}
	return &a, nil
}

// sellerOffers returns the offer keys starting with the seller address
// (DEx_hasOffer and DEx_getTokenForSale match on the key prefix).
func (s *State) sellerOffers(seller string) ([]string, error) {
	kvs, err := s.tx.Scan(NsOffer, seller)
	keys := make([]string, len(kvs))
	for i, kv := range kvs {
		keys[i] = kv.Key
	}
	return keys, err
}

// tokenForSale mirrors DEx_getTokenForSale.
func (s *State) tokenForSale(seller string) (uint32, bool, error) {
	keys, err := s.sellerOffers(seller)
	if err != nil || len(keys) == 0 {
		return 0, false, err
	}
	parts := strings.FieldsFunc(keys[0], func(r rune) bool { return r == '-' })
	if len(parts) != 2 {
		return 0, false, nil
	}
	id, err := strconv.ParseUint(parts[1], 10, 32)
	if err != nil {
		return 0, false, nil
	}
	return uint32(id), true, nil
}

func (s *State) move(addr string, id uint32, amount int64, from, to int) error {
	if err := s.mustUpdate(addr, id, -amount, from); err != nil {
		return err
	}
	return s.mustUpdate(addr, id, amount, to)
}

// offerCreate mirrors DEx_offerCreate.
func (s *State) offerCreate(seller string, id uint32, amount int64, block int64, desired, minFee int64, window uint8, txid string) (int, error) {
	if window == 0 || desired == 0 {
		return dexErrorSellOffer - 101, nil
	}
	if o, err := s.Offer(seller, id); err != nil || o != nil {
		return dexErrorSellOffer - 10, err
	}
	if free, err := s.FeatureActive(chain.FeatureFreeDEx, block); err != nil {
		return 0, err
	} else if free {
		if keys, err := s.sellerOffers(seller); err != nil || len(keys) > 0 {
			return dexErrorSellOffer - 10, err
		}
	}
	bal, err := s.Balance(seller, id, Balance)
	if err != nil {
		return 0, err
	}
	if amount > bal {
		return dexErrorSellOffer - 25, nil
	}
	if amount <= 0 {
		return dexErrorSellOffer, nil
	}
	if err := s.move(seller, id, amount, Balance, SellOfferReserve); err != nil {
		return 0, err
	}
	return 0, s.putJSON(NsOffer, offerKey(seller, id), &Offer{
		Block: block, AmountOriginal: amount, Property: id, LTCDesired: desired,
		MinFee: minFee, BlockTimeLimit: window, Txid: txid,
	})
}

// offerDestroy mirrors DEx_offerDestroy.
func (s *State) offerDestroy(seller string, id uint32) (int, error) {
	if o, err := s.Offer(seller, id); err != nil || o == nil {
		return dexErrorSellOffer - 11, err
	}
	reserved, err := s.Balance(seller, id, SellOfferReserve)
	if err != nil {
		return 0, err
	}
	if reserved > 0 {
		if err := s.move(seller, id, reserved, SellOfferReserve, Balance); err != nil {
			return 0, err
		}
	}
	return 0, s.tx.Delete(NsOffer, offerKey(seller, id))
}

func (s *State) offerUpdate(seller string, id uint32, amount int64, block int64, desired, minFee int64, window uint8, txid string) (int, error) {
	if o, err := s.Offer(seller, id); err != nil || o == nil {
		return dexErrorSellOffer - 12, err
	}
	rc, err := s.offerDestroy(seller, id)
	if err != nil || rc != 0 {
		return rc, err
	}
	return s.offerCreate(seller, id, amount, block, desired, minFee, window, txid)
}

// Sub-actions of DEx offers version 1.
const (
	dexNew    = 1
	dexUpdate = 2
	dexCancel = 3
)

// logicMath_TradeOffer.
func (s *State) tradeOffer(t *Tx) (int, error) {
	m := t.Msg
	if ok, err := s.typeAllowed(t.Block, m.Property, m.Type, m.Version); err != nil || !ok {
		return pktErrorOffer - 22, err
	}
	if nft, err := s.nonFungible(m.Property); err != nil || nft {
		return pktErrorNFT - 21, err
	}
	if m.Amount > maxInt8Bytes {
		return pktErrorOffer - 23, nil
	}
	if free, err := s.FeatureActive(chain.FeatureFreeDEx, t.Block); err != nil {
		return 0, err
	} else if !free && m.Property != PropertyOmni && m.Property != PropertyTestOmni {
		return pktErrorOffer - 47, nil
	}

	amount := int64(m.Amount)
	desired, minFee := int64(m.AmountDesired), int64(m.MinFee)
	exists, err := s.Offer(t.Sender, m.Property)
	if err != nil {
		return 0, err
	}
	create := func() (int, error) {
		return s.offerCreate(t.Sender, m.Property, amount, t.Block, desired, minFee, m.BlockTimeLimit, t.Txid)
	}
	update := func() (int, error) {
		return s.offerUpdate(t.Sender, m.Property, amount, t.Block, desired, minFee, m.BlockTimeLimit, t.Txid)
	}

	switch m.Version {
	case 0:
		switch {
		case amount != 0 && exists == nil:
			return create()
		case amount != 0:
			return update()
		case exists != nil:
			return s.offerDestroy(t.Sender, m.Property)
		}
		return pktErrorOffer - 49, nil
	case 1:
		if exists != nil && m.Subaction != dexCancel && m.Subaction != dexUpdate {
			return pktErrorOffer - 48, nil
		}
		if exists == nil && m.Subaction != dexNew {
			return pktErrorOffer - 49, nil
		}
		switch m.Subaction {
		case dexNew:
			return create()
		case dexUpdate:
			return update()
		case dexCancel:
			return s.offerDestroy(t.Sender, m.Property)
		}
		return pktError - 999, nil
	}
	return pktError - 500, nil
}

// logicMath_AcceptOffer_BTC and DEx_acceptCreate.
func (s *State) acceptOffer(t *Tx, res *Result) (int, error) {
	m := t.Msg
	if ok, err := s.typeAllowed(t.Block, m.Property, m.Type, m.Version); err != nil || !ok {
		return dexErrorAccept - 22, err
	}
	if nft, err := s.nonFungible(m.Property); err != nil || nft {
		return pktErrorNFT - 21, err
	}
	if !validAmount(m.Amount) {
		return dexErrorAccept - 23, nil
	}
	buyer, seller := t.Sender, m.Receiver
	offer, err := s.Offer(seller, m.Property)
	if err != nil || offer == nil {
		return dexErrorAccept - 15, err
	}
	if a, err := s.accept(seller, buyer, m.Property); err != nil || a != nil {
		return dexErrorAccept - 205, err
	}
	if t.Fee < offer.MinFee {
		return dexErrorAccept - 105, nil
	}
	remaining, err := s.Balance(seller, m.Property, SellOfferReserve)
	if err != nil {
		return 0, err
	}
	reserved := min(int64(m.Amount), remaining)
	res.NewAmount = uint64(max(reserved, 0))
	if reserved <= 0 {
		return dexErrorAccept - 10, nil
	}
	if err := s.move(seller, m.Property, reserved, SellOfferReserve, AcceptReserve); err != nil {
		return 0, err
	}
	return 0, s.putJSON(NsAccept, acceptKey(seller, buyer, m.Property), &Accept{
		AmountOriginal: reserved, AmountRemaining: reserved, BlockTimeLimit: offer.BlockTimeLimit,
		Property: offer.Property, OfferAmountOriginal: offer.AmountOriginal,
		LTCDesiredOriginal: offer.LTCDesired, OfferTxid: offer.Txid, Block: t.Block,
	})
}

// acceptDestroy mirrors DEx_acceptDestroy: the remaining reserve goes back
// to the offer if it is still the same offer, otherwise to the balance.
func (s *State) acceptDestroy(seller, buyer string, id uint32, a *Accept) error {
	offer, err := s.Offer(seller, id)
	if err != nil {
		return err
	}
	to := Balance
	if offer != nil && offer.Txid == a.OfferTxid {
		to = SellOfferReserve
	}
	if a.AmountRemaining > 0 {
		if err := s.move(seller, id, a.AmountRemaining, AcceptReserve, to); err != nil {
			return err
		}
	}
	return s.tx.Delete(NsAccept, acceptKey(seller, buyer, id))
}

// dexPayment mirrors DEx_payment for one output. It reports the tokens
// bought, if any.
func (s *State) dexPayment(seller, buyer string, paid int64, block int64) (uint32, int64, error) {
	var id uint32
	var a *Accept
	free, err := s.FeatureActive(chain.FeatureFreeDEx, block)
	if err != nil {
		return 0, 0, err
	}
	if !free {
		for _, p := range []uint32{PropertyOmni, PropertyTestOmni} {
			if a, err = s.accept(seller, buyer, p); err != nil {
				return 0, 0, err
			} else if a != nil {
				id = p
				break
			}
		}
	} else if p, ok, err := s.tokenForSale(seller); err != nil {
		return 0, 0, err
	} else if ok {
		id = p
		if a, err = s.accept(seller, buyer, p); err != nil {
			return 0, 0, err
		}
	}
	if a == nil || a.LTCDesiredOriginal == 0 {
		return 0, 0, nil
	}

	amount := dexPurchase(a.OfferAmountOriginal, a.LTCDesiredOriginal, paid)
	if a.AmountRemaining < amount {
		amount = a.AmountRemaining
	}
	if amount > 0 {
		if err := s.mustUpdate(seller, id, -amount, AcceptReserve); err != nil {
			return 0, 0, err
		}
		if err := s.mustUpdate(buyer, id, amount, Balance); err != nil {
			return 0, 0, err
		}
	}

	done := amount >= a.AmountRemaining
	a.AmountRemaining -= amount
	if !done {
		return id, amount, s.putJSON(NsAccept, acceptKey(seller, buyer, id), a)
	}
	sellReserve, err := s.Balance(seller, id, SellOfferReserve)
	if err != nil {
		return 0, 0, err
	}
	acceptReserve, err := s.Balance(seller, id, AcceptReserve)
	if err != nil {
		return 0, 0, err
	}
	if err := s.acceptDestroy(seller, buyer, id, a); err != nil {
		return 0, 0, err
	}
	if sellReserve == 0 && acceptReserve == 0 {
		if _, err := s.offerDestroy(seller, id); err != nil {
			return 0, 0, err
		}
	}
	return id, amount, nil
}

// dexPurchase mirrors calculateDExPurchase: paid * offered / desired, rounded up.
func dexPurchase(offered, desired, paid int64) int64 {
	p := new(big.Int).Mul(bi(paid), bi(offered))
	if p.Sign() != 0 {
		p.Sub(p, bi(1))
		p.Quo(p, bi(desired))
		p.Add(p, bi(1))
	}
	return convertTo64(p)
}

// Payment is a Class A transaction without data that may pay DEx accepts.
type Payment struct {
	Txid    string
	Block   int64
	Sender  string
	Outputs []omni.DExPayment
}

// ApplyPayment mirrors HandleDExPayments. The result is recorded only
// when at least one output bought tokens, as in OmniLite.
func (s *State) ApplyPayment(p *Payment) (*Result, error) {
	res := &Result{}
	for _, o := range p.Outputs {
		id, amount, err := s.dexPayment(o.Address, p.Sender, o.Value, p.Block)
		if err != nil {
			return nil, fmt.Errorf("payment %s: %w", p.Txid, err)
		}
		if amount > 0 {
			res.Payments = append(res.Payments, Purchase{Vout: o.Vout, Seller: o.Address, Property: id, Amount: amount})
		}
	}
	if len(res.Payments) == 0 {
		return nil, nil
	}
	res.Valid = true
	return res, s.putJSON(NsResult, p.Txid, res)
}

// expireAccepts mirrors eraseExpiredAccepts at the end of a block.
func (s *State) expireAccepts(height int64) error {
	kvs, err := s.tx.Scan(NsAccept, "")
	if err != nil {
		return err
	}
	for _, kv := range kvs {
		var a Accept
		if err := json.Unmarshal(kv.Value, &a); err != nil {
			return err
		}
		if height-a.Block < int64(a.BlockTimeLimit) {
			continue
		}
		seller, rest, _ := strings.Cut(kv.Key, "-")
		_, buyer, _ := strings.Cut(rest, "+")
		if err := s.acceptDestroy(seller, buyer, a.Property, &a); err != nil {
			return err
		}
	}
	return nil
}
