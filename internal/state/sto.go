package state

import (
	"fmt"
	"math/big"
	"sort"
)

// STO fees per receiver (sto.h), paid in OMNI or TOMNI.
const (
	stoFeePerOwner   = 1
	stoFeePerOwnerV1 = 1000
)

// STOReceive is one receiver of a Send To Owners.
type STOReceive struct {
	Address string `json:"address"`
	Amount  int64  `json:"amount"`
}

type owner struct {
	amount int64
	addr   string
}

// sortOwners orders like OwnerAddrType iterated in reverse: larger
// amounts first, equal amounts by ascending address.
func sortOwners(o []owner) {
	sort.Slice(o, func(i, j int) bool {
		if o[i].amount != o[j].amount {
			return o[i].amount > o[j].amount
		}
		return o[i].addr < o[j].addr
	})
}

// stoReceivers mirrors STO_GetReceivers: holders of property, without the
// sender, each getting their share rounded up until the amount is used up.
func (s *State) stoReceivers(sender string, property uint32, amount int64) ([]owner, error) {
	holders, err := s.tx.Scan(NsHolder, propKey(property)+"/")
	if err != nil {
		return nil, err
	}
	var owners []owner
	var total int64
	for _, kv := range holders {
		addr := kv.Key[len(propKey(property))+1:]
		if addr == sender {
			continue
		}
		t, err := s.tally(addr, property)
		if err != nil {
			return nil, err
		}
		tokens := t[Balance] + t[SellOfferReserve] + t[AcceptReserve]
		total += tokens
		if tokens > 0 {
			owners = append(owners, owner{tokens, addr})
		}
	}
	sortOwners(owners)

	var receivers []owner
	var sent int64
	for _, o := range owners {
		piece := new(big.Int).Mul(bi(o.amount), bi(amount))
		if piece.Sign() > 0 {
			// DivideAndRoundUp
			piece.Sub(piece, bi(1))
			piece.Quo(piece, bi(total))
			piece.Add(piece, bi(1))
		}
		should := convertTo64(piece)
		receive := should
		if amount-sent < should {
			receive = amount - sent
		}
		sent += receive
		if receive <= 0 {
			break
		}
		receivers = append(receivers, owner{receive, o.addr})
	}
	sortOwners(receivers)
	return receivers, nil
}

// logicMath_SendToOwners.
func (s *State) sendToOwners(t *Tx, res *Result) (int, error) {
	m := t.Msg
	if ok, err := s.typeAllowed(t.Block, m.Property, m.Type, m.Version); err != nil || !ok {
		return pktErrorSTO - 22, err
	}
	if nft, err := s.nonFungible(m.Property); err != nil || nft {
		return pktErrorNFT - 21, err
	}
	if !validAmount(m.Amount) {
		return pktErrorSTO - 23, nil
	}
	if ok, err := s.validPropertyID(m.Property); err != nil || !ok {
		return pktErrorSTO - 24, err
	}
	if m.Version > 0 {
		if ok, err := s.validPropertyID(m.DistributionProperty); err != nil || !ok {
			return pktErrorSTO - 24, err
		}
	}
	amount := int64(m.Amount)
	if bal, err := s.Balance(t.Sender, m.Property, Balance); err != nil || bal < amount {
		return pktErrorSTO - 25, err
	}

	distributeTo := m.Property
	if m.Version > 0 {
		distributeTo = m.DistributionProperty
	}
	receivers, err := s.stoReceivers(t.Sender, distributeTo, amount)
	if err != nil {
		return 0, err
	}
	if len(receivers) == 0 {
		return pktErrorSTO - 26, nil
	}

	feeProperty := PropertyOmni
	if isTestEcosystem(m.Property) {
		feeProperty = PropertyTestOmni
	}
	feePerOwner := int64(stoFeePerOwner)
	if m.Version > 0 {
		feePerOwner = stoFeePerOwnerV1
	}
	fee := feePerOwner * int64(len(receivers))
	feeBal, err := s.Balance(t.Sender, feeProperty, Balance)
	if err != nil {
		return 0, err
	}
	if feeProperty != m.Property {
		if feeBal < fee {
			return pktErrorSTO - 27, nil
		}
	} else if feeBal < amount+fee {
		return pktErrorSTO - 28, nil
	}

	if err := s.mustUpdate(t.Sender, feeProperty, -fee, Balance); err != nil {
		return 0, err
	}
	var sent int64
	for _, r := range receivers {
		sent += r.amount
		if err := s.mustUpdate(t.Sender, m.Property, -r.amount, Balance); err != nil {
			return 0, err
		}
		if err := s.mustUpdate(r.addr, m.Property, r.amount, Balance); err != nil {
			return 0, err
		}
		res.STO = append(res.STO, STOReceive{Address: r.addr, Amount: r.amount})
	}
	if sent != amount {
		return 0, fmt.Errorf("send to owners distributed %d of %d", sent, amount)
	}
	return 0, nil
}
