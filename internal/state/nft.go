package state

import (
	"fmt"
	"math"
	"sort"

	"github.com/tbuuol/omnilite-standalone/internal/chain"
)

// Storage types of the non-fungible token database (NonFungibleStorage).
const (
	nftRangeIndex byte = 'R' // owner of each range
	nftGrantData  byte = 'G'
	nftIssuerData byte = 'I'
	nftHolderData byte = 'H'
)

// NFTRange is a range of tokens with its owner or data.
type NFTRange struct {
	Start, End int64
	Value      string
}

func nftPrefix(id uint32, typ byte) string { return fmt.Sprintf("%010d_%d_", id, typ) }

func nftKey(id uint32, typ byte, start, end int64) string {
	return fmt.Sprintf("%s%020d-%020d", nftPrefix(id, typ), start, end)
}

// NFTRanges returns the ranges of one storage type in database order.
func (s *State) NFTRanges(id uint32, typ byte) ([]NFTRange, error) {
	kvs, err := s.tx.Scan(NsNFT, nftPrefix(id, typ))
	if err != nil {
		return nil, err
	}
	res := make([]NFTRange, 0, len(kvs))
	for _, kv := range kvs {
		var r NFTRange
		if _, err := fmt.Sscanf(kv.Key[len(nftPrefix(id, typ)):], "%d-%d", &r.Start, &r.End); err != nil {
			return nil, fmt.Errorf("nft key %q: %w", kv.Key, err)
		}
		r.Value = string(kv.Value)
		res = append(res, r)
	}
	return res, nil
}

// nftFind returns the first range in database order that holds token
// (GetRange, GetNonFungibleTokenOwner, GetNonFungibleTokenData).
func (s *State) nftFind(id uint32, token int64, typ byte) (NFTRange, bool, error) {
	ranges, err := s.NFTRanges(id, typ)
	if err != nil {
		return NFTRange{}, false, err
	}
	for _, r := range ranges {
		if token >= r.Start && token <= r.End {
			return r, true, nil
		}
	}
	return NFTRange{}, false, nil
}

func (s *State) nftOwner(id uint32, token int64) (string, error) {
	r, _, err := s.nftFind(id, token, nftRangeIndex)
	return r.Value, err
}

// nftContiguous mirrors IsRangeContiguous: start and end lie in the same
// owner range.
func (s *State) nftContiguous(id uint32, start, end int64) (bool, error) {
	r, ok, err := s.nftFind(id, start, nftRangeIndex)
	if err != nil || !ok {
		return false, err
	}
	return end >= start && end <= r.End, nil
}

func (s *State) nftAdd(id uint32, typ byte, start, end int64, value string) error {
	return s.tx.Put(NsNFT, nftKey(id, typ, start, end), []byte(value))
}

func (s *State) nftDelete(id uint32, typ byte, start, end int64) error {
	return s.tx.Delete(NsNFT, nftKey(id, typ, start, end))
}

// nftOwnedRange checks that from owns start..end as one range.
func (s *State) nftOwnedRange(id uint32, start, end int64, from string) (bool, error) {
	startOwner, err := s.nftOwner(id, start)
	if err != nil {
		return false, err
	}
	endOwner, err := s.nftOwner(id, end)
	if err != nil {
		return false, err
	}
	contiguous, err := s.nftContiguous(id, start, end)
	return startOwner == from && endOwner == from && contiguous, err
}

// nftMove mirrors MoveNonFungibleTokens, including the merge with
// adjacent ranges of the receiver.
func (s *State) nftMove(id uint32, start, end int64, from, to string) error {
	if ok, err := s.nftOwnedRange(id, start, end, from); err != nil || !ok {
		if err == nil {
			err = fmt.Errorf("nft move %d %d-%d: not owned by %s", id, start, end, from)
		}
		return err
	}
	senderRange, _, err := s.nftFind(id, start, nftRangeIndex)
	if err != nil {
		return err
	}
	complete := senderRange.Start == start && senderRange.End == end
	below, err := s.nftOwner(id, start-1)
	if err != nil {
		return err
	}
	after, err := s.nftOwner(id, end+1)
	if err != nil {
		return err
	}
	if err := s.nftDelete(id, nftRangeIndex, senderRange.Start, senderRange.End); err != nil {
		return err
	}
	if !complete {
		if senderRange.Start < start {
			if err := s.nftAdd(id, nftRangeIndex, senderRange.Start, start-1, from); err != nil {
				return err
			}
		}
		if senderRange.End > end {
			if err := s.nftAdd(id, nftRangeIndex, end+1, senderRange.End, from); err != nil {
				return err
			}
		}
	}
	newStart, newEnd := start, end
	if below == to {
		old, _, err := s.nftFind(id, start-1, nftRangeIndex)
		if err != nil {
			return err
		}
		newStart = old.Start
		if err := s.nftDelete(id, nftRangeIndex, old.Start, old.End); err != nil {
			return err
		}
	}
	if after == to {
		old, _, err := s.nftFind(id, end+1, nftRangeIndex)
		if err != nil {
			return err
		}
		newEnd = old.End
		if err := s.nftDelete(id, nftRangeIndex, old.Start, old.End); err != nil {
			return err
		}
	}
	return s.nftAdd(id, nftRangeIndex, newStart, newEnd, to)
}

// nftSetData mirrors ChangeNonFungibleTokenData. Like the original it only
// replaces ranges found contiguously from start on.
func (s *State) nftSetData(id uint32, start, end int64, data string, typ byte) error {
	var found []NFTRange
	for i := start; i <= end; {
		r, ok, err := s.nftFind(id, i, typ)
		if err != nil {
			return err
		}
		if !ok {
			break
		}
		found = append(found, r)
		if r.End == math.MaxInt64 {
			break
		}
		i = r.End + 1
	}
	if len(found) > 0 {
		// std::set orders the ranges by (start, end) and drops duplicates.
		sort.Slice(found, func(i, j int) bool {
			if found[i].Start != found[j].Start {
				return found[i].Start < found[j].Start
			}
			return found[i].End < found[j].End
		})
		first, last := found[0], found[len(found)-1]
		// The kept data is read at the start token of the first and the last range.
		before, _, err := s.nftFind(id, first.Start, typ)
		if err != nil {
			return err
		}
		after, _, err := s.nftFind(id, last.Start, typ)
		if err != nil {
			return err
		}
		for _, r := range found {
			if err := s.nftDelete(id, typ, r.Start, r.End); err != nil {
				return err
			}
		}
		if first.Start < start {
			if err := s.nftAdd(id, typ, first.Start, start-1, before.Value); err != nil {
				return err
			}
		}
		if last.End > end {
			if err := s.nftAdd(id, typ, end+1, last.End, after.Value); err != nil {
				return err
			}
		}
	}
	return s.nftAdd(id, typ, start, end, data)
}

func (s *State) nftHighestEnd(id uint32) (int64, error) {
	ranges, err := s.NFTRanges(id, nftRangeIndex)
	var high int64
	for _, r := range ranges {
		if r.End > high {
			high = r.End
		}
	}
	return high, err
}

// nftCreate mirrors CreateNonFungibleTokens and returns the granted range.
func (s *State) nftCreate(id uint32, amount int64, owner, info string) (int64, int64, error) {
	high, err := s.nftHighestEnd(id)
	if err != nil {
		return 0, 0, err
	}
	start, end := high+1, high+amount
	if high > math.MaxInt64-amount {
		end = math.MaxInt64
	}
	if err := s.nftAdd(id, nftGrantData, start, end, info); err != nil {
		return 0, 0, err
	}
	ownerStart := start
	if prevOwner, err := s.nftOwner(id, high); err != nil {
		return 0, 0, err
	} else if prevOwner == owner {
		old, _, err := s.nftFind(id, high, nftRangeIndex)
		if err != nil {
			return 0, 0, err
		}
		if err := s.nftDelete(id, nftRangeIndex, old.Start, old.End); err != nil {
			return 0, 0, err
		}
		ownerStart = old.Start
	}
	return start, end, s.nftAdd(id, nftRangeIndex, ownerStart, end, owner)
}

// checkTokenRange runs the range checks shared by types 5 and 201.
func (s *State) checkTokenRange(t *Tx) (int, error) {
	m := t.Msg
	if ok, err := s.typeAllowed(t.Block, m.Property, m.Type, m.Version); err != nil || !ok {
		return pktErrorNFT - 23, err
	}
	if nft, err := s.nonFungible(m.Property); err != nil || !nft {
		return pktErrorNFT - 24, err
	}
	if !validAmount(m.TokenStart) {
		return pktErrorNFT - 25, nil
	}
	if !validAmount(m.TokenEnd) {
		return pktErrorNFT - 26, nil
	}
	if m.TokenStart > m.TokenEnd {
		return pktErrorNFT - 27, nil
	}
	if int64(m.TokenEnd-m.TokenStart)+1 <= 0 {
		return pktErrorNFT - 28, nil
	}
	if p, err := s.Property(m.Property); err != nil || p == nil {
		return pktErrorNFT - 29, err
	}
	return 0, nil
}

// logicMath_SendNonFungible. GetAvailableTokenBalance also subtracts the
// node's own pending sends; that is wallet state, not consensus, and zero here.
func (s *State) sendNonFungible(t *Tx) (int, error) {
	if code, err := s.checkTokenRange(t); code != 0 || err != nil {
		return code, err
	}
	m := t.Msg
	start, end := int64(m.TokenStart), int64(m.TokenEnd)
	amount := end - start + 1
	bal, err := s.Balance(t.Sender, m.Property, Balance)
	if err != nil || bal < amount {
		return pktErrorNFT - 32, err
	}
	if ok, err := s.nftOwnedRange(m.Property, start, end, t.Sender); err != nil || !ok {
		return pktErrorNFT - 33, err
	}
	to := m.Receiver
	if to == "" {
		to = t.Sender
	}
	if err := s.mustUpdate(t.Sender, m.Property, -amount, Balance); err != nil {
		return 0, err
	}
	if err := s.mustUpdate(to, m.Property, amount, Balance); err != nil {
		return 0, err
	}
	return 0, s.nftMove(m.Property, start, end, t.Sender, to)
}

// logicMath_NonFungibleData.
func (s *State) nonFungibleData(t *Tx) (int, error) {
	if code, err := s.checkTokenRange(t); code != 0 || err != nil {
		return code, err
	}
	m := t.Msg
	start, end := int64(m.TokenStart), int64(m.TokenEnd)
	typ := nftHolderData
	if m.NFTDataType != 0 {
		typ = nftIssuerData
	}
	if typ == nftHolderData {
		if ok, err := s.nftOwnedRange(m.Property, start, end, t.Sender); err != nil || !ok {
			return pktErrorNFT - 30, err
		}
	} else {
		p, err := s.property(m.Property)
		if err != nil {
			return 0, err
		}
		if p.IssuerAt(t.Block) != t.Sender {
			// Before feature 18 anyone may set issuer data.
			if active, err := s.FeatureActive(chain.FeatureNonFungibleIssuer, t.Block); err != nil || active {
				return pktErrorNFT - 31, err
			}
		}
	}
	return 0, s.nftSetData(m.Property, start, end, m.NFTData, typ)
}
