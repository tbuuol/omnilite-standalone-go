package state

import (
	"encoding/json"
	"fmt"
	"math"
	"math/big"
)

// Crowdsale is an active crowdsale (CMPCrowd), keyed by its issuer address.
type Crowdsale struct {
	PropertyID      uint32         `json:"property"`
	Amount          int64          `json:"amount"`
	PropertyDesired uint32         `json:"desired"`
	Deadline        int64          `json:"deadline"`
	EarlyBird       uint8          `json:"earlybird"`
	Percentage      uint8          `json:"percentage"`
	UserCreated     int64          `json:"usercreated"`
	IssuerCreated   int64          `json:"issuercreated"`
	Purchases       []HistoryEntry `json:"purchases,omitempty"`
}

// Crowdsale returns the active crowdsale of an issuer address (getCrowd).
func (s *State) Crowdsale(addr string) (*Crowdsale, error) {
	v, ok, err := s.tx.Get(NsCrowd, addr)
	if err != nil || !ok {
		return nil, err
	}
	var c Crowdsale
	return &c, json.Unmarshal(v, &c)
}

func (s *State) putCrowdsale(addr string, c *Crowdsale) error {
	v, err := json.Marshal(c)
	if err != nil {
		return err
	}
	return s.tx.Put(NsCrowd, addr, v)
}

var maxInt64 = big.NewInt(math.MaxInt64)

func bi(v int64) *big.Int { return big.NewInt(v) }

// convertTo64 mirrors ConvertTo64, which asserts the value fits.
func convertTo64(v *big.Int) int64 {
	if v.Sign() < 0 || v.Cmp(maxInt64) > 0 {
		panic("ConvertTo64 out of range: " + v.String())
	}
	return v.Int64()
}

// missedIssuerBonus mirrors GetMissedIssuerBonus.
func missedIssuerBonus(p *Property, c *Crowdsale) int64 {
	credited := bi(c.IssuerCreated)
	users := bi(c.UserCreated)
	total := new(big.Int).Add(users, credited)
	exact := new(big.Int).Mul(users, bi(int64(p.Percentage)))
	exact.Quo(exact, bi(100))
	if exact.Cmp(credited) < 0 {
		return 0
	}
	missing := new(big.Int)
	if exact.Cmp(credited) > 0 {
		missing.Sub(exact, credited)
	}
	if new(big.Int).Add(total, missing).Cmp(maxInt64) > 0 {
		missing.Sub(maxInt64, total)
	}
	return convertTo64(missing)
}

// calculateFundraiser mirrors calculateFundraiser: tokens for the buyer
// and the issuer, and whether the crowdsale hit the token limit.
func calculateFundraiser(inflate bool, amount int64, bonusPerc uint8, deadline, now, perUnit int64, issuerPerc uint8, totalTokens int64) (user, issuer int64, closeSale bool) {
	weekSecs := bi(604800)
	precision := bi(1000000000000)
	pctPrecision := bi(100)
	satoshi := bi(100000000)

	bonusSecs := new(big.Int)
	if now < deadline {
		bonusSecs.Sub(bi(deadline), bi(now))
	}
	weeks := new(big.Int).Quo(bonusSecs, weekSecs)
	weeks.Mul(weeks, precision)
	rem := new(big.Int).Rem(bonusSecs, weekSecs)
	rem.Mul(rem, precision)
	rem.Quo(rem, weekSecs)
	weeks.Add(weeks, rem)

	ebPercentage := new(big.Int).Mul(weeks, bi(int64(bonusPerc)))
	bonusPercentage := new(big.Int).Mul(precision, pctPrecision)
	bonusPercentage.Add(bonusPercentage, ebPercentage)
	bonusPercentage.Quo(bonusPercentage, pctPrecision)

	issuerPercentage := bi(int64(issuerPerc))
	issuerPercentage.Mul(issuerPercentage, precision)
	issuerPercentage.Quo(issuerPercentage, pctPrecision)

	created := bi(amount)
	if inflate {
		created.Mul(created, satoshi)
	}
	created.Mul(created, bi(perUnit))
	created.Mul(created, bonusPercentage)

	issuerTokens := new(big.Int).Quo(created, satoshi)
	issuerTokens.Quo(issuerTokens, precision)
	issuerTokens.Mul(issuerTokens, new(big.Int).Quo(issuerPercentage, bi(100)))
	issuerTokens.Mul(issuerTokens, precision)

	createdInt := new(big.Int).Quo(created, precision)
	createdInt.Quo(createdInt, satoshi)

	issuerInt := new(big.Int).Quo(issuerTokens, precision)
	issuerInt.Quo(issuerInt, satoshi)
	issuerInt.Quo(issuerInt, bi(100))

	newTotal := new(big.Int).Add(bi(totalTokens), createdInt)
	newTotal.Add(newTotal, issuerInt)
	if newTotal.Cmp(maxInt64) > 0 {
		maxCreatable := new(big.Int).Sub(maxInt64, bi(totalTokens))
		sum := new(big.Int).Add(createdInt, issuerInt)
		ratio := new(big.Int).Mul(sum, precision)
		ratio.Mul(ratio, satoshi)
		ratio.Quo(ratio, maxCreatable)

		issuerInt.Mul(issuerInt, precision)
		issuerInt.Mul(issuerInt, satoshi)
		issuerInt.Quo(issuerInt, ratio)
		createdInt.Sub(maxCreatable, issuerInt)
		closeSale = true
	}
	return convertTo64(createdInt), convertTo64(issuerInt), closeSale
}

// logicMath_CreatePropertyVariable.
func (s *State) createCrowdsale(t *Tx) (int, error) {
	m := t.Msg
	if m.Ecosystem != 1 && m.Ecosystem != 2 {
		return pktErrorSP - 21, nil
	}
	if isTestEcosystem(uint32(m.Ecosystem)) != isTestEcosystem(m.Property) {
		return pktErrorSP - 50, nil
	}
	if ok, err := s.typeAllowed(t.Block, uint32(m.Ecosystem), m.Type, m.Version); err != nil || !ok {
		return pktErrorSP - 22, err
	}
	if !validAmount(m.Amount) {
		return pktErrorSP - 23, nil
	}
	if ok, err := s.validPropertyID(m.Property); err != nil || !ok {
		return pktErrorSP - 24, err
	}
	if m.PropType != PropTypeIndivisible && m.PropType != PropTypeDivisible {
		return pktErrorSP - 36, nil
	}
	if m.Name == "" {
		return pktErrorSP - 37, nil
	}
	if m.Deadline == 0 || int64(m.Deadline) < t.BlockTime {
		return pktErrorSP - 38, nil
	}
	if c, err := s.Crowdsale(t.Sender); err != nil || c != nil {
		return pktErrorSP - 39, err
	}
	p := newProperty(t)
	p.NumTokens = int64(m.Amount)
	p.PropertyDesired = m.Property
	p.Deadline = int64(m.Deadline)
	p.EarlyBird, p.Percentage = m.EarlyBird, m.Percentage
	id, err := s.putNewProperty(m.Ecosystem, p)
	if err != nil {
		return 0, err
	}
	return 0, s.putCrowdsale(t.Sender, &Crowdsale{
		PropertyID: id, Amount: int64(m.Amount), PropertyDesired: m.Property,
		Deadline: int64(m.Deadline), EarlyBird: m.EarlyBird, Percentage: m.Percentage,
	})
}

// logicMath_CloseCrowdsale.
func (s *State) closeCrowdsale(t *Tx) (int, error) {
	m := t.Msg
	if ok, err := s.typeAllowed(t.Block, m.Property, m.Type, m.Version); err != nil || !ok {
		return pktErrorSP - 22, err
	}
	if nft, err := s.nonFungible(m.Property); err != nil || nft {
		return pktErrorNFT - 21, err
	}
	if ok, err := s.validPropertyID(m.Property); err != nil || !ok {
		return pktErrorSP - 24, err
	}
	c, err := s.Crowdsale(t.Sender)
	if err != nil || c == nil {
		return pktErrorSP - 40, err
	}
	if m.Property != c.PropertyID {
		return pktErrorSP - 41, nil
	}
	p, err := s.property(m.Property)
	if err != nil {
		return 0, err
	}
	missed, err := s.missedBonus(p, c)
	if err != nil {
		return 0, err
	}
	p.History = c.Purchases
	p.UpdateBlock = t.BlockHash
	p.CloseEarly = true
	p.TimeClosed = t.BlockTime
	p.TxidClose = t.Txid
	p.MissedTokens = missed
	if err := s.putProperty(p); err != nil {
		return 0, err
	}
	if missed > 0 {
		if err := s.mustUpdate(p.Issuer, m.Property, missed, Balance); err != nil {
			return 0, err
		}
	}
	return 0, s.tx.Delete(NsCrowd, t.Sender)
}

// missedBonus runs GetMissedIssuerBonus with its consistency check.
func (s *State) missedBonus(p *Property, c *Crowdsale) (int64, error) {
	total, err := s.TotalTokens(c.PropertyID)
	if err != nil {
		return 0, err
	}
	if total != c.UserCreated+c.IssuerCreated {
		return 0, fmt.Errorf("crowdsale %d: %d tokens exist, %d created", c.PropertyID, total, c.UserCreated+c.IssuerCreated)
	}
	return missedIssuerBonus(p, c), nil
}

// crowdsaleParticipation mirrors logicHelper_CrowdsaleParticipation after
// a simple send. Its return code does not change the send's result.
func (s *State) crowdsaleParticipation(t *Tx, res *Result) error {
	m := t.Msg
	c, err := s.Crowdsale(m.Receiver)
	if err != nil || c == nil || c.PropertyDesired != m.Property {
		return err
	}
	p, err := s.property(c.PropertyID)
	if err != nil {
		return err
	}
	desired, err := s.Property(m.Property)
	if err != nil {
		return err
	}
	inflate := desired != nil && !desired.Divisible()
	total, err := s.TotalTokens(c.PropertyID)
	if err != nil {
		return err
	}
	user, issuer, closeSale := calculateFundraiser(inflate, int64(m.Amount), p.EarlyBird, p.Deadline, t.BlockTime, p.NumTokens, p.Percentage, total)
	c.UserCreated += user
	c.IssuerCreated += issuer
	c.Purchases = insertHistory(c.Purchases, HistoryEntry{Txid: t.Txid, Data: []int64{int64(m.Amount), t.BlockTime, user, issuer}})
	res.CrowdProperty, res.CrowdTokens, res.CrowdIssuer = c.PropertyID, user, issuer
	if user > 0 {
		if err := s.mustUpdate(t.Sender, c.PropertyID, user, Balance); err != nil {
			return err
		}
	}
	if issuer > 0 {
		if err := s.mustUpdate(m.Receiver, c.PropertyID, issuer, Balance); err != nil {
			return err
		}
	}
	if !closeSale {
		return s.putCrowdsale(m.Receiver, c)
	}
	// eraseMaxedCrowdsale
	p.History = c.Purchases
	p.CloseEarly = true
	p.MaxTokens = true
	p.TimeClosed = t.BlockTime
	p.UpdateBlock = t.BlockHash
	if err := s.putProperty(p); err != nil {
		return err
	}
	return s.tx.Delete(NsCrowd, m.Receiver)
}

// expireCrowdsales mirrors eraseExpiredCrowdsale at the start of a block.
func (s *State) expireCrowdsales(blockTime int64, blockHash string) error {
	kvs, err := s.tx.Scan(NsCrowd, "")
	if err != nil {
		return err
	}
	for _, kv := range kvs {
		var c Crowdsale
		if err := json.Unmarshal(kv.Value, &c); err != nil {
			return err
		}
		if blockTime <= c.Deadline {
			continue
		}
		p, err := s.property(c.PropertyID)
		if err != nil {
			return err
		}
		missed, err := s.missedBonus(p, &c)
		if err != nil {
			return err
		}
		p.History = c.Purchases
		p.MissedTokens = missed
		p.UpdateBlock = blockHash
		if err := s.putProperty(p); err != nil {
			return err
		}
		if missed > 0 {
			if err := s.mustUpdate(p.Issuer, c.PropertyID, missed, Balance); err != nil {
				return err
			}
		}
		if err := s.tx.Delete(NsCrowd, kv.Key); err != nil {
			return err
		}
	}
	return nil
}
