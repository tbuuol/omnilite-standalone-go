package state

import (
	"encoding/json"
	"fmt"
	"strconv"

	"github.com/tbuuol/omnilite-standalone/internal/chain"
	"github.com/tbuuol/omnilite-standalone/internal/omni"
)

// FeatureBlock returns the current activation block of a feature, or false
// for unknown features.
func (s *State) FeatureBlock(id uint16) (int64, bool, error) {
	def, known := s.params.Features[id]
	if !known {
		return 0, false, nil
	}
	v, ok, err := s.tx.Get(NsFeature, strconv.Itoa(int(id)))
	if err != nil || !ok {
		return def, true, err
	}
	n, err := strconv.ParseInt(string(v), 10, 64)
	return n, true, err
}

func (s *State) setFeatureBlock(id uint16, block int64) error {
	return s.tx.Put(NsFeature, strconv.Itoa(int(id)), strconv.AppendInt(nil, block, 10))
}

// FeatureActive mirrors IsFeatureActivated.
func (s *State) FeatureActive(id uint16, block int64) (bool, error) {
	at, known, err := s.FeatureBlock(id)
	return known && block >= at, err
}

// restriction is one row of GetRestrictions. feature, if set, gives the
// activation block; all other rows activate at or below the Omni genesis
// block, where parsing starts.
type restriction struct {
	txType, version uint16
	wildcard        bool
	feature         uint16
}

var restrictions = []restriction{
	{omni.TypeAlert, 0xFFFF, true, 0},
	{omni.TypeActivation, 0xFFFF, true, 0},
	{omni.TypeDeactivation, 0xFFFF, true, 0},
	{omni.TypeSimpleSend, 0, false, 0},
	{omni.TypeTradeOffer, 0, false, 0},
	{omni.TypeTradeOffer, 1, false, 0},
	{omni.TypeAcceptOffer, 0, false, 0},
	{omni.TypeCreatePropertyFixed, 0, false, 0},
	{omni.TypeCreatePropertyVar, 0, false, 0},
	{omni.TypeCreatePropertyVar, 1, false, 0},
	{omni.TypeCloseCrowdsale, 0, false, 0},
	{omni.TypeCreatePropertyMan, 0, false, 0},
	{omni.TypeGrantTokens, 0, false, 0},
	{omni.TypeRevokeTokens, 0, false, 0},
	{omni.TypeChangeIssuer, 0, false, 0},
	{omni.TypeEnableFreezing, 0, false, 0},
	{omni.TypeDisableFreezing, 0, false, 0},
	{omni.TypeFreezeTokens, 0, false, 0},
	{omni.TypeUnfreezeTokens, 0, false, 0},
	{omni.TypeSendToOwners, 0, false, 0},
	{omni.TypeSendToOwners, 1, false, chain.FeatureSTOv1},
	{omni.TypeSendAll, 0, false, 0},
	{omni.TypeAnyData, 0, true, 0},
	{omni.TypeSendNonFungible, 0, false, chain.FeatureNonFungible},
	{omni.TypeNonFungibleData, 0, false, chain.FeatureNonFungible},
}

// typeAllowed mirrors IsTransactionTypeAllowed. Property 0 is a wildcard
// only where the row allows it; the test ecosystem has no height limit.
func (s *State) typeAllowed(block int64, property uint32, txType, version uint16) (bool, error) {
	for _, r := range restrictions {
		if r.txType != txType || r.version != version {
			continue
		}
		if property == PropertyLTC && !r.wildcard {
			continue
		}
		if isTestEcosystem(property) {
			return true, nil
		}
		at := int64(0)
		if r.feature != 0 {
			var err error
			if at, _, err = s.FeatureBlock(r.feature); err != nil {
				return false, err
			}
		}
		if block >= at {
			return true, nil
		}
	}
	return false, nil
}

// FeatureName mirrors GetFeatureName.
func FeatureName(id uint16) string {
	switch id {
	case chain.FeatureSTOv1:
		return "Cross-property Send To Owners"
	case chain.FeatureFreezeNotice:
		return "Activate the waiting period for enabling freezing"
	case chain.FeatureFreeDEx:
		return "Activate trading of any token on the distributed exchange"
	case chain.FeatureNonFungible:
		return "Uniquely identifiable tokens"
	case chain.FeatureNonFungibleIssuer:
		return "NFT issuer data update by issuers only"
	}
	return "Unknown feature"
}

// Activation is a scheduled or completed feature activation.
type Activation struct {
	FeatureID        uint16 `json:"featureid"`
	Name             string `json:"featurename"`
	Block            int64  `json:"activationblock"`
	MinClientVersion uint32 `json:"minimumversion"`
}

func pendingKey(id uint16) string { return fmt.Sprintf("p/%05d", id) }

// activate mirrors ActivateFeature.
func (s *State) activate(id uint16, at int64, minVersion uint32, block int64, alertKey string) (bool, error) {
	if at < block+s.params.MinActivationBlocks || at > block+s.params.MaxActivationBlocks {
		return false, nil
	}
	if active, err := s.FeatureActive(id, block); err != nil || active {
		return false, err
	}
	supported := OmniCoreVersion >= int64(minVersion)
	if _, known := s.params.Features[id]; known {
		if err := s.setFeatureBlock(id, at); err != nil {
			return false, err
		}
	} else {
		supported = false
	}

	// AddPendingActivation replaces an earlier schedule of the same feature.
	a := Activation{FeatureID: id, Name: FeatureName(id), Block: at, MinClientVersion: minVersion}
	v, err := json.Marshal(a)
	if err != nil {
		return false, err
	}
	if err := s.tx.Put(NsActivation, pendingKey(id), v); err != nil {
		return false, err
	}
	if !supported {
		text := fmt.Sprintf("Your client must be updated and will shutdown at block %d (unsupported feature %d ('%s') activated)\n", at, id, a.Name)
		if err := s.addAlert(alertKey, "omnicore", alertBlockExpiry, uint32(at), text); err != nil {
			return false, err
		}
	}
	return true, nil
}

// deactivate mirrors DeactivateFeature.
func (s *State) deactivate(id uint16, block int64, alertKey string) (bool, error) {
	if active, err := s.FeatureActive(id, block); err != nil || !active {
		return false, err
	}
	off := int64(chain.NotActive)
	switch id {
	case chain.FeatureSTOv1, chain.FeatureFreezeNotice, chain.FeatureFreeDEx, chain.FeatureNonFungible:
	case chain.FeatureNonFungibleIssuer:
		off = 999999 // sic, as in rules.cpp
	default:
		return false, nil
	}
	if err := s.setFeatureBlock(id, off); err != nil {
		return false, err
	}
	text := fmt.Sprintf("An emergency deactivation of feature ID %d (%s) has occurred.", id, FeatureName(id))
	return true, s.addAlert(alertKey, "omnicore", alertBlockExpiry, uint32(block+1024), text)
}

// ErrUnsupportedActivation stops the indexer like OmniLite shuts down when
// a feature goes live that needs a newer client.
type ErrUnsupportedActivation struct{ Activation Activation }

func (e *ErrUnsupportedActivation) Error() string {
	return fmt.Sprintf("feature %d (%s) went live at block %d and needs client version %d; this indexer implements %d",
		e.Activation.FeatureID, e.Activation.Name, e.Activation.Block, e.Activation.MinClientVersion, OmniCoreVersion)
}

// BeginBlock runs the start-of-block handling (CheckLiveActivations).
func (s *State) BeginBlock(height int64) error {
	pending, err := s.tx.Scan(NsActivation, "p/")
	if err != nil {
		return err
	}
	for _, kv := range pending {
		var a Activation
		if err := json.Unmarshal(kv.Value, &a); err != nil {
			return err
		}
		if a.Block > height {
			continue
		}
		if OmniCoreVersion < int64(a.MinClientVersion) {
			return &ErrUnsupportedActivation{a}
		}
		if err := s.tx.Delete(NsActivation, kv.Key); err != nil {
			return err
		}
		if err := s.tx.Put(NsActivation, fmt.Sprintf("c/%010d/%05d", a.Block, a.FeatureID), kv.Value); err != nil {
			return err
		}
	}
	return nil
}

// Alert types (notifications.h).
const (
	alertBlockExpiry         = 1
	alertBlockTimeExpiry     = 2
	alertClientVersionExpiry = 3
)

// Alert is an active alert.
type Alert struct {
	Sender  string `json:"sender"`
	Type    uint16 `json:"type"`
	Expiry  uint32 `json:"expiry"`
	Message []byte `json:"message"`
}

// addAlert mirrors AddAlert. key keeps alerts in the order they were added.
func (s *State) addAlert(key, sender string, typ uint16, expiry uint32, text string) error {
	if sender != "omnicore" && (typ < alertBlockExpiry || typ > alertClientVersionExpiry) {
		return nil
	}
	v, err := json.Marshal(Alert{Sender: sender, Type: typ, Expiry: expiry, Message: []byte(text)})
	if err != nil {
		return err
	}
	return s.tx.Put(NsAlert, key, v)
}

func (s *State) deleteAlerts(sender string) error {
	all, err := s.tx.Scan(NsAlert, "")
	if err != nil {
		return err
	}
	for _, kv := range all {
		var a Alert
		if err := json.Unmarshal(kv.Value, &a); err != nil {
			return err
		}
		if a.Sender == sender {
			if err := s.tx.Delete(NsAlert, kv.Key); err != nil {
				return err
			}
		}
	}
	return nil
}

// EndBlock runs the end-of-block handling (CheckExpiredAlerts).
func (s *State) EndBlock(height, blockTime int64) error {
	all, err := s.tx.Scan(NsAlert, "")
	if err != nil {
		return err
	}
	for _, kv := range all {
		var a Alert
		if err := json.Unmarshal(kv.Value, &a); err != nil {
			return err
		}
		var expired bool
		switch a.Type {
		case alertBlockExpiry:
			expired = height >= int64(a.Expiry)
		case alertBlockTimeExpiry:
			expired = blockTime > int64(a.Expiry)
		case alertClientVersionExpiry:
			expired = OmniCoreVersion > int64(a.Expiry)
		default:
			expired = true
		}
		if expired {
			if err := s.tx.Delete(NsAlert, kv.Key); err != nil {
				return err
			}
		}
	}
	return nil
}
