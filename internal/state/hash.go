package state

import (
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"hash"
	"sort"
)

// ConsensusHash mirrors GetConsensusHash: SHA256 over balances, DEx
// offers and accepts, active crowdsales and property issuers, in the
// order OmniLite writes them. The result is printed like uint256::GetHex,
// i.e. byte-reversed.
func (s *State) ConsensusHash() (string, error) {
	h := sha256.New()
	write := func(format string, args ...any) { fmt.Fprintf(h, format, args...) }

	// Stage 1: balances, by address and then property.
	bals, err := s.tx.Scan(NsBalance, "")
	if err != nil {
		return "", err
	}
	for _, kv := range bals {
		addr, id := kv.Key[:len(kv.Key)-11], kv.Key[len(kv.Key)-10:]
		var t [tallyTypes]int64
		for i := range t {
			t[i] = int64(binary.BigEndian.Uint64(kv.Value[8*i:]))
		}
		if t[Balance] == 0 && t[SellOfferReserve] == 0 && t[AcceptReserve] == 0 {
			continue
		}
		var n uint64
		fmt.Sscanf(id, "%d", &n)
		write("%s|%d|%d|%d|%d", addr, n, t[Balance], t[SellOfferReserve], t[AcceptReserve])
	}

	if err := s.hashOffers(h); err != nil {
		return "", err
	}
	if err := s.hashAccepts(h); err != nil {
		return "", err
	}
	if err := s.hashCrowdsales(h); err != nil {
		return "", err
	}

	// Stage 5: issuers of all properties of both ecosystems.
	for _, eco := range []uint8{1, 2} {
		first := PropertyOmni
		if eco == 2 {
			first = TestEcoFirst
		}
		next, err := s.nextID(eco)
		if err != nil {
			return "", err
		}
		for id := first; id < next; id++ {
			p, err := s.Property(id)
			if err != nil {
				return "", err
			}
			if p != nil {
				write("%d|%s", id, p.Issuer)
			}
		}
	}

	sum := h.Sum(nil)
	for i, j := 0, len(sum)-1; i < j; i, j = i+1, j-1 {
		sum[i], sum[j] = sum[j], sum[i]
	}
	return hex.EncodeToString(sum), nil
}

// Stage 2: sell offers by txid. The seller is cut from the map key by
// dropping two characters, which is only right for one-digit property IDs;
// OmniLite hashes it that way.
func (s *State) hashOffers(h hash.Hash) error {
	kvs, err := s.tx.Scan(NsOffer, "")
	if err != nil {
		return err
	}
	type entry struct{ txid, data string }
	var list []entry
	for _, kv := range kvs {
		var o Offer
		if err := json.Unmarshal(kv.Value, &o); err != nil {
			return err
		}
		seller := kv.Key[:max(len(kv.Key)-2, 0)]
		list = append(list, entry{o.Txid, fmt.Sprintf("%s|%s|%d|%d|%d|%d|%d",
			o.Txid, seller, o.Property, o.AmountOriginal, o.LTCDesired, o.MinFee, o.BlockTimeLimit)})
	}
	sort.SliceStable(list, func(i, j int) bool { return list[i].txid < list[j].txid })
	for _, e := range list {
		h.Write([]byte(e.data))
	}
	return nil
}

// Stage 3: accepts by offer txid and buyer.
func (s *State) hashAccepts(h hash.Hash) error {
	kvs, err := s.tx.Scan(NsAccept, "")
	if err != nil {
		return err
	}
	type entry struct{ key, data string }
	var list []entry
	for _, kv := range kvs {
		var a Accept
		if err := json.Unmarshal(kv.Value, &a); err != nil {
			return err
		}
		buyer := kv.Key
		for i := 0; i < len(kv.Key); i++ {
			if kv.Key[i] == '+' {
				buyer = kv.Key[i+1:]
				break
			}
		}
		list = append(list, entry{a.OfferTxid + "-" + buyer, fmt.Sprintf("%s|%s|%d|%d|%d",
			a.OfferTxid, buyer, a.AmountOriginal, a.AmountRemaining, a.Block)})
	}
	sort.Slice(list, func(i, j int) bool {
		if list[i].key != list[j].key {
			return list[i].key < list[j].key
		}
		return list[i].data < list[j].data
	})
	for _, e := range list {
		h.Write([]byte(e.data))
	}
	return nil
}

// Stage 4: active crowdsales by property ID.
func (s *State) hashCrowdsales(h hash.Hash) error {
	kvs, err := s.tx.Scan(NsCrowd, "")
	if err != nil {
		return err
	}
	type entry struct {
		id   uint32
		data string
	}
	var list []entry
	for _, kv := range kvs {
		var c Crowdsale
		if err := json.Unmarshal(kv.Value, &c); err != nil {
			return err
		}
		list = append(list, entry{c.PropertyID, fmt.Sprintf("%d|%d|%d|%d|%d",
			c.PropertyID, c.PropertyDesired, c.Deadline, c.UserCreated, c.IssuerCreated)})
	}
	sort.Slice(list, func(i, j int) bool {
		if list[i].id != list[j].id {
			return list[i].id < list[j].id
		}
		return list[i].data < list[j].data
	})
	for _, e := range list {
		h.Write([]byte(e.data))
	}
	return nil
}
