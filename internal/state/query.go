package state

import (
	"encoding/json"
	"fmt"
	"math/big"
	"strconv"
	"strings"
)

// Read access for the API. Everything here only reads.

// Tally returns balance, sell-offer reserve and accept reserve.
func (s *State) Tally(addr string, id uint32) ([3]int64, error) {
	t, err := s.tally(addr, id)
	return [3]int64(t), err
}

// HasTally reports whether the address ever had a tally (getTally != nullptr).
func (s *State) HasTally(addr string) (bool, error) { return s.hasTally(addr) }

// Frozen reports whether an address is frozen for a property.
func (s *State) Frozen(addr string, id uint32) (bool, error) { return s.frozen(addr, id) }

// NextID returns the next free property ID of an ecosystem (peekNextSPID).
func (s *State) NextID(ecosystem uint8) (uint32, error) { return s.nextID(ecosystem) }

// ValidPropertyID mirrors IsPropertyIdValid.
func (s *State) ValidPropertyID(id uint32) (bool, error) { return s.validPropertyID(id) }

// Divisible mirrors isPropertyDivisible: unknown properties count as divisible.
func (s *State) Divisible(id uint32) (bool, error) {
	p, err := s.Property(id)
	return p == nil || p.Divisible(), err
}

// PropertyByTx mirrors findSPByTX.
func (s *State) PropertyByTx(txid string) (uint32, error) {
	v, ok, err := s.tx.Get(NsPropertyTx, txid)
	if err != nil || !ok {
		return 0, err
	}
	n, err := strconv.ParseUint(string(v), 10, 32)
	return uint32(n), err
}

// Holders returns every address that ever had a tally entry for a
// property, in address order.
func (s *State) Holders(id uint32) ([]string, error) {
	kvs, err := s.tx.Scan(NsHolder, propKey(id)+"/")
	addrs := make([]string, len(kvs))
	for i, kv := range kvs {
		addrs[i] = kv.Key[len(propKey(id))+1:]
	}
	return addrs, err
}

// AddressProperties returns the properties an address has tally entries
// for, in ascending order.
func (s *State) AddressProperties(addr string) ([]uint32, error) {
	kvs, err := s.tx.Scan(NsBalance, addr+"/")
	if err != nil {
		return nil, err
	}
	ids := make([]uint32, 0, len(kvs))
	for _, kv := range kvs {
		n, err := strconv.ParseUint(kv.Key[len(addr)+1:], 10, 32)
		if err != nil {
			return nil, err
		}
		ids = append(ids, uint32(n))
	}
	return ids, nil
}

// Result returns the recorded result of a transaction, or nil.
func (s *State) Result(txid string) (*Result, error) {
	var r Result
	ok, err := s.getJSON(NsResult, txid, &r)
	if err != nil || !ok {
		return nil, err
	}
	return &r, nil
}

// ActiveCrowdsale pairs a crowdsale with its issuer address.
type ActiveCrowdsale struct {
	Issuer string
	*Crowdsale
}

// Crowdsales returns the active crowdsales in issuer address order (my_crowds).
func (s *State) Crowdsales() ([]ActiveCrowdsale, error) {
	kvs, err := s.tx.Scan(NsCrowd, "")
	if err != nil {
		return nil, err
	}
	res := make([]ActiveCrowdsale, 0, len(kvs))
	for _, kv := range kvs {
		var c Crowdsale
		if err := json.Unmarshal(kv.Value, &c); err != nil {
			return nil, err
		}
		res = append(res, ActiveCrowdsale{kv.Key, &c})
	}
	return res, nil
}

// OfferEntry is a sell offer with its map key "seller-property".
type OfferEntry struct {
	Key string
	*Offer
}

// Offers returns all sell offers in key order (my_offers).
func (s *State) Offers() ([]OfferEntry, error) {
	kvs, err := s.tx.Scan(NsOffer, "")
	if err != nil {
		return nil, err
	}
	res := make([]OfferEntry, 0, len(kvs))
	for _, kv := range kvs {
		var o Offer
		if err := json.Unmarshal(kv.Value, &o); err != nil {
			return nil, err
		}
		res = append(res, OfferEntry{kv.Key, &o})
	}
	return res, nil
}

// AcceptEntry is an accept with its buyer.
type AcceptEntry struct {
	Seller string
	Buyer  string
	*Accept
}

// Accepts returns all accepts in key order (my_accepts).
func (s *State) Accepts() ([]AcceptEntry, error) {
	kvs, err := s.tx.Scan(NsAccept, "")
	if err != nil {
		return nil, err
	}
	res := make([]AcceptEntry, 0, len(kvs))
	for _, kv := range kvs {
		var a Accept
		if err := json.Unmarshal(kv.Value, &a); err != nil {
			return nil, err
		}
		seller, rest, _ := strings.Cut(kv.Key, "-")
		_, buyer, _ := strings.Cut(rest, "+")
		res = append(res, AcceptEntry{Seller: seller, Buyer: buyer, Accept: &a})
	}
	return res, nil
}

// DesiredLTC mirrors calculateDesiredBTC: desired * available / offered,
// rounded up.
func DesiredLTC(offered, desired, available int64) int64 {
	if offered == 0 {
		return 0
	}
	p := new(big.Int).Mul(bi(desired), bi(available))
	if p.Sign() != 0 {
		p.Sub(p, bi(1))
		p.Quo(p, bi(offered))
		p.Add(p, bi(1))
	}
	return convertTo64(p)
}

// Activations returns the pending and completed feature activations.
func (s *State) Activations() (pending, completed []Activation, err error) {
	for _, part := range []struct {
		prefix string
		out    *[]Activation
	}{{"p/", &pending}, {"c/", &completed}} {
		kvs, err := s.tx.Scan(NsActivation, part.prefix)
		if err != nil {
			return nil, nil, err
		}
		for _, kv := range kvs {
			var a Activation
			if err := json.Unmarshal(kv.Value, &a); err != nil {
				return nil, nil, err
			}
			*part.out = append(*part.out, a)
		}
	}
	return pending, completed, nil
}

// Alerts returns the active alerts in the order they were added.
func (s *State) Alerts() ([]Alert, error) {
	kvs, err := s.tx.Scan(NsAlert, "")
	if err != nil {
		return nil, err
	}
	res := make([]Alert, 0, len(kvs))
	for _, kv := range kvs {
		var a Alert
		if err := json.Unmarshal(kv.Value, &a); err != nil {
			return nil, err
		}
		res = append(res, a)
	}
	return res, nil
}

// NFT storage types for the API.
const (
	NFTOwners     = nftRangeIndex
	NFTGrantData  = nftGrantData
	NFTIssuerData = nftIssuerData
	NFTHolderData = nftHolderData
)

// NFTValue returns the owner or data of one token, "" if unset.
func (s *State) NFTValue(id uint32, token int64, typ byte) (string, error) {
	r, _, err := s.nftFind(id, token, typ)
	return r.Value, err
}

// AddressNFTs mirrors GetAddressNonFungibleTokens: the owner ranges of an
// address, per property (0: all properties).
func (s *State) AddressNFTs(id uint32, addr string) (map[uint32][]NFTRange, []uint32, error) {
	kvs, err := s.tx.Scan(NsNFT, "")
	if err != nil {
		return nil, nil, err
	}
	res := map[uint32][]NFTRange{}
	var order []uint32
	for _, kv := range kvs {
		if string(kv.Value) != addr {
			continue
		}
		var pid uint32
		var typ byte
		var r NFTRange
		if _, err := fmt.Sscanf(kv.Key, "%10d_%d_%d-%d", &pid, &typ, &r.Start, &r.End); err != nil {
			return nil, nil, fmt.Errorf("nft key %q: %w", kv.Key, err)
		}
		if (id != 0 && id != pid) || typ != nftRangeIndex {
			continue
		}
		if _, seen := res[pid]; !seen {
			order = append(order, pid)
		}
		r.Value = addr
		res[pid] = append(res[pid], r)
	}
	return res, order, nil
}

// NFTOwner mirrors GetNonFungibleTokenOwner.
func (s *State) NFTOwner(id uint32, token int64) (string, error) { return s.nftOwner(id, token) }

// NFTContiguous mirrors IsRangeContiguous.
func (s *State) NFTContiguous(id uint32, start, end int64) (bool, error) {
	return s.nftContiguous(id, start, end)
}

// HasOffer mirrors DEx_hasOffer.
func (s *State) HasOffer(seller string) (bool, error) {
	keys, err := s.sellerOffers(seller)
	return len(keys) > 0, err
}

// Accept mirrors DEx_getAccept; nil if there is none.
func (s *State) Accept(seller, buyer string, id uint32) (*Accept, error) {
	return s.accept(seller, buyer, id)
}
