package state

import (
	"fmt"
	"strings"

	"github.com/tbuuol/omnilite-standalone/internal/chain"
)

func freezingKey(id uint32, liveBlock int64) string {
	return fmt.Sprintf("%s/%010d", propKey(id), liveBlock)
}

// freezingBlocks returns the live blocks of the freezing entries of a
// property in ascending order (setFreezingEnabledProperties).
func (s *State) freezingBlocks(id uint32) ([]int64, error) {
	kvs, err := s.tx.Scan(NsFreezing, propKey(id)+"/")
	if err != nil {
		return nil, err
	}
	var blocks []int64
	for _, kv := range kvs {
		var b int64
		if _, err := fmt.Sscanf(kv.Key[len(propKey(id))+1:], "%d", &b); err != nil {
			return nil, err
		}
		blocks = append(blocks, b)
	}
	return blocks, nil
}

// FreezingEnabled mirrors isFreezingEnabled.
func (s *State) FreezingEnabled(id uint32, block int64) (bool, error) {
	blocks, err := s.freezingBlocks(id)
	for _, b := range blocks {
		if block >= b {
			return true, err
		}
	}
	return false, err
}

// disableFreezing removes the freezing entry with the highest live block
// and unfreezes every address of the property.
func (s *State) disableFreezing(id uint32) error {
	blocks, err := s.freezingBlocks(id)
	if err != nil {
		return err
	}
	if len(blocks) == 0 {
		return fmt.Errorf("disable freezing of property %d without entry", id)
	}
	if err := s.tx.Delete(NsFreezing, freezingKey(id, blocks[len(blocks)-1])); err != nil {
		return err
	}
	frozen, err := s.tx.Scan(NsFrozen, "")
	if err != nil {
		return err
	}
	suffix := "/" + propKey(id)
	for _, kv := range frozen {
		if strings.HasSuffix(kv.Key, suffix) {
			if err := s.tx.Delete(NsFrozen, kv.Key); err != nil {
				return err
			}
		}
	}
	return nil
}

// managedByIssuer runs the checks shared by the freeze transactions.
func (s *State) managedByIssuer(t *Tx) (int, error) {
	m := t.Msg
	if ok, err := s.typeAllowed(t.Block, m.Property, m.Type, m.Version); err != nil || !ok {
		return pktErrorTokens - 22, err
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
	return 0, nil
}

// logicMath_EnableFreezing.
func (s *State) enableFreezing(t *Tx) (int, error) {
	if code, err := s.managedByIssuer(t); code != 0 || err != nil {
		return code, err
	}
	id := t.Msg.Property
	if on, err := s.FreezingEnabled(id, t.Block); err != nil || on {
		return pktErrorTokens - 49, err
	}
	live := t.Block
	notice, err := s.FeatureActive(chain.FeatureFreezeNotice, t.Block)
	if err != nil {
		return 0, err
	}
	if notice {
		live += s.params.FreezeWaitPeriod
	}
	return 0, s.tx.Put(NsFreezing, freezingKey(id, live), nil)
}

// logicMath_DisableFreezing.
func (s *State) disableFreezingTx(t *Tx) (int, error) {
	if code, err := s.managedByIssuer(t); code != 0 || err != nil {
		return code, err
	}
	if on, err := s.FreezingEnabled(t.Msg.Property, t.Block); err != nil || !on {
		return pktErrorTokens - 47, err
	}
	return 0, s.disableFreezing(t.Msg.Property)
}

// logicMath_FreezeTokens and logicMath_UnfreezeTokens.
func (s *State) freeze(t *Tx, freeze bool) (int, error) {
	if code, err := s.managedByIssuer(t); code != 0 || err != nil {
		return code, err
	}
	m := t.Msg
	if on, err := s.FreezingEnabled(m.Property, t.Block); err != nil || !on {
		return pktErrorTokens - 47, err
	}
	frozen, err := s.frozen(m.Receiver, m.Property)
	if err != nil {
		return 0, err
	}
	key := balanceKey(m.Receiver, m.Property)
	if freeze {
		if frozen {
			return pktErrorTokens - 50, nil
		}
		return 0, s.tx.Put(NsFrozen, key, nil)
	}
	if !frozen {
		return pktErrorTokens - 48, nil
	}
	return 0, s.tx.Delete(NsFrozen, key)
}
