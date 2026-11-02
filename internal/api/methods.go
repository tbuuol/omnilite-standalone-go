package api

import (
	"encoding/binary"
	"encoding/hex"
	"fmt"
	"math"

	"github.com/tbuuol/omnilite-standalone/internal/indexer"
	"github.com/tbuuol/omnilite-standalone/internal/state"
)

type handler func(q *query, a args) (any, error)

var methods map[string]handler

func init() {
	methods = map[string]handler{
		"omni_getinfo":                   getInfo,
		"omni_getbalance":                getBalance,
		"omni_getallbalancesforid":       getAllBalancesForID,
		"omni_getallbalancesforaddress":  getAllBalancesForAddress,
		"omni_getproperty":               getProperty,
		"omni_listproperties":            listProperties,
		"omni_getcrowdsale":              getCrowdsale,
		"omni_getactivecrowdsales":       getActiveCrowdsales,
		"omni_getgrants":                 getGrants,
		"omni_getactivedexsells":         getActiveDExSells,
		"omni_listblocktransactions":     listBlockTransactions,
		"omni_listblockstransactions":    listBlocksTransactions,
		"omni_gettransaction":            getTransaction,
		"omni_getsto":                    getSTO,
		"omni_getactivations":            getActivations,
		"omni_getcurrentconsensushash":   getCurrentConsensusHash,
		"omni_getnonfungibletokens":      getNonFungibleTokens,
		"omni_getnonfungibletokendata":   getNonFungibleTokenData,
		"omni_getnonfungibletokenranges": getNonFungibleTokenRanges,
		"omni_createpayload_dexsell":     createPayloadDExSell,
	}
}

// OmniCoreVersionString is OmniCoreVersion() of OmniLite 0.18.1.
const OmniCoreVersionString = "0.9.1"

// NodeVersion is filled in at start-up with the node's version string.
var NodeVersion = ""

func getInfo(q *query, a args) (any, error) {
	if err := a.count(0, 0, "omni_getinfo"); err != nil {
		return nil, err
	}
	tip, err := q.snap.Tip()
	if err != nil {
		return nil, err
	}
	var height, blockTime int64
	if tip != nil {
		height, blockTime = tip.Height, tip.Time
	}
	inBlock, err := q.snap.Scan(indexer.NsBlockTx, fmt.Sprintf("%010d:", height))
	if err != nil {
		return nil, err
	}
	total, err := indexer.TxCount(q.snap)
	if err != nil {
		return nil, err
	}
	alerts, err := q.st.Alerts()
	if err != nil {
		return nil, err
	}
	list := array{}
	for _, al := range alerts {
		typ := "error"
		switch al.Type {
		case 1:
			typ = "alertexpiringbyblock"
		case 2:
			typ = "alertexpiringbyblocktime"
		case 3:
			typ = "alertexpiringbyclientversion"
		}
		list = append(list, (&object{}).
			add("alerttypeint", al.Type).
			add("alerttype", typ).
			add("alertexpiry", formatIndivisible(int64(al.Expiry))).
			add("alertmessage", string(al.Message)))
	}
	return (&object{}).
		add("omnicoreversion_int", state.OmniCoreVersion).
		add("omnicoreversion", OmniCoreVersionString).
		add("litecoincoreversion", NodeVersion).
		add("block", height).
		add("blocktime", blockTime).
		add("blocktransactions", len(inBlock)).
		add("totaltransactions", total).
		add("alerts", list), nil
}

// balanceObject mirrors BalanceToJSON and reports whether any amount is set.
func (q *query) balanceObject(o *object, addr string, id uint32, divisible bool) (bool, error) {
	t, err := q.st.Tally(addr, id)
	if err != nil {
		return false, err
	}
	frozen, err := q.st.Frozen(addr, id)
	if err != nil {
		return false, err
	}
	available, reserved := t[state.Balance], t[state.SellOfferReserve]+t[state.AcceptReserve]
	var frozenAmount int64
	if frozen {
		frozenAmount = available
	}
	f := formatIndivisible
	if divisible {
		f = formatDivisible
	}
	o.add("balance", f(available)).add("reserved", f(reserved)).add("frozen", f(frozenAmount))
	return available != 0 || reserved != 0 || frozenAmount != 0, nil
}

func getBalance(q *query, a args) (any, error) {
	if err := a.count(2, 2, "omni_getbalance \"address\" propertyid"); err != nil {
		return nil, err
	}
	addr, err := q.address(a, 0)
	if err != nil {
		return nil, err
	}
	id, err := a.propertyID(1)
	if err != nil {
		return nil, err
	}
	p, err := q.requireExisting(id)
	if err != nil {
		return nil, err
	}
	o := &object{}
	_, err = q.balanceObject(o, addr, id, p.Divisible())
	return o, err
}

func getAllBalancesForID(q *query, a args) (any, error) {
	if err := a.count(1, 1, "omni_getallbalancesforid propertyid"); err != nil {
		return nil, err
	}
	id, err := a.propertyID(0)
	if err != nil {
		return nil, err
	}
	p, err := q.requireExisting(id)
	if err != nil {
		return nil, err
	}
	holders, err := q.st.Holders(id)
	if err != nil {
		return nil, err
	}
	// OmniLite walks an unordered map; this lists by address.
	res := array{}
	for _, addr := range holders {
		o := (&object{}).add("address", addr)
		nonEmpty, err := q.balanceObject(o, addr, id, p.Divisible())
		if err != nil {
			return nil, err
		}
		if nonEmpty {
			res = append(res, o)
		}
	}
	return res, nil
}

func getAllBalancesForAddress(q *query, a args) (any, error) {
	if err := a.count(1, 1, "omni_getallbalancesforaddress \"address\""); err != nil {
		return nil, err
	}
	addr, err := q.address(a, 0)
	if err != nil {
		return nil, err
	}
	if ok, err := q.st.HasTally(addr); err != nil {
		return nil, err
	} else if !ok {
		return nil, errorf(codeInvalidParam, "Address not found")
	}
	ids, err := q.st.AddressProperties(addr)
	if err != nil {
		return nil, err
	}
	res := array{}
	for _, id := range ids {
		p, err := q.st.Property(id)
		if err != nil {
			return nil, err
		}
		if p == nil {
			continue
		}
		o := (&object{}).add("propertyid", id).add("name", string(p.Name))
		nonEmpty, err := q.balanceObject(o, addr, id, p.Divisible())
		if err != nil {
			return nil, err
		}
		if nonEmpty {
			res = append(res, o)
		}
	}
	return res, nil
}

// propertyFields mirrors PropertyToJSON.
func propertyFields(o *object, p *state.Property) {
	o.add("name", string(p.Name)).
		add("category", string(p.Category)).
		add("subcategory", string(p.Subcategory)).
		add("data", string(p.Data)).
		add("url", string(p.URL)).
		add("divisible", p.Divisible()).
		add("issuer", p.Issuer).
		add("creationtxid", hash256(p.Txid)).
		add("fixedissuance", p.Fixed).
		add("managedissuance", p.Manual).
		add("non-fungibletoken", p.Unique)
}

// hash256 prints a txid like uint256::GetHex, all zeros when unset.
func hash256(txid string) string {
	if txid == "" {
		return "0000000000000000000000000000000000000000000000000000000000000000"
	}
	return txid
}

func getProperty(q *query, a args) (any, error) {
	if err := a.count(1, 1, "omni_getproperty propertyid"); err != nil {
		return nil, err
	}
	id, err := a.propertyID(0)
	if err != nil {
		return nil, err
	}
	p, err := q.requireExisting(id)
	if err != nil {
		return nil, err
	}
	total, err := q.st.TotalTokens(id)
	if err != nil {
		return nil, err
	}
	o := (&object{}).add("propertyid", id)
	propertyFields(o, p)
	if p.Manual {
		h, err := q.height()
		if err != nil {
			return nil, err
		}
		on, err := q.st.FreezingEnabled(id, h)
		if err != nil {
			return nil, err
		}
		o.add("freezingenabled", on)
	}
	totalStr, err := q.formatMP(id, total)
	if err != nil {
		return nil, err
	}
	return o.add("totaltokens", totalStr), nil
}

func listProperties(q *query, a args) (any, error) {
	res := array{}
	for _, eco := range []uint8{1, 2} {
		first := state.PropertyOmni
		if eco == 2 {
			first = state.TestEcoFirst
		}
		next, err := q.st.NextID(eco)
		if err != nil {
			return nil, err
		}
		for id := first; id < next; id++ {
			p, err := q.st.Property(id)
			if err != nil {
				return nil, err
			}
			if p == nil {
				continue
			}
			o := (&object{}).add("propertyid", id)
			propertyFields(o, p)
			res = append(res, o)
		}
	}
	return res, nil
}

// creationTime returns the block time of a property's creation tx (-1 if unknown).
func (q *query) creationTime(txid string) (int64, error) {
	rec, err := q.record(txid)
	if err != nil || rec == nil {
		return -1, err
	}
	return rec.Time, nil
}

func (q *query) activeCrowdsale(id uint32) (*state.ActiveCrowdsale, error) {
	crowds, err := q.st.Crowdsales()
	if err != nil {
		return nil, err
	}
	for i := range crowds {
		if crowds[i].PropertyID == id {
			return &crowds[i], nil
		}
	}
	return nil, nil
}

func getCrowdsale(q *query, a args) (any, error) {
	if err := a.count(1, 2, "omni_getcrowdsale propertyid ( verbose )"); err != nil {
		return nil, err
	}
	id, err := a.propertyID(0)
	if err != nil {
		return nil, err
	}
	verbose := false
	if a.has(1) {
		if verbose, err = a.boolean(1); err != nil {
			return nil, err
		}
	}
	p, err := q.requireExisting(id)
	if err != nil {
		return nil, err
	}
	if p.Fixed || p.Manual {
		return nil, errorf(codeInvalidParam, "Property identifier does not refer to a crowdsale")
	}
	start, err := q.creationTime(p.Txid)
	if err != nil {
		return nil, err
	}
	active, err := q.activeCrowdsale(id)
	if err != nil {
		return nil, err
	}
	history := p.History
	if active != nil {
		history = active.Purchases
	}
	total, err := q.st.TotalTokens(id)
	if err != nil {
		return nil, err
	}
	desiredDiv, err := q.st.Divisible(p.PropertyDesired)
	if err != nil {
		return nil, err
	}
	desiredType, ownType := state.PropTypeIndivisible, state.PropTypeIndivisible
	if desiredDiv {
		desiredType = state.PropTypeDivisible
	}
	if p.Divisible() {
		ownType = state.PropTypeDivisible
	}

	var raised, issuerTokens int64
	type participant struct {
		key string
		obj *object
	}
	var parts []participant
	for _, h := range history {
		raised += h.Data[0]
		issuerTokens += h.Data[3]
		parts = append(parts, participant{fmt.Sprintf("%d-%s", h.Data[1], h.Txid), (&object{}).
			add("txid", h.Txid).
			add("amountsent", formatByType(h.Data[0], desiredType)).
			add("participanttokens", formatByType(h.Data[2], ownType)).
			add("issuertokens", formatByType(h.Data[3], ownType))})
	}

	mp := func(prop uint32, n int64) string {
		s, _ := q.formatMP(prop, n)
		return s
	}
	o := (&object{}).
		add("propertyid", id).
		add("name", string(p.Name)).
		add("active", active != nil).
		add("issuer", p.Issuer).
		add("propertyiddesired", p.PropertyDesired).
		add("tokensperunit", mp(id, p.NumTokens)).
		add("earlybonus", p.EarlyBird).
		add("percenttoissuer", p.Percentage).
		add("starttime", start).
		add("deadline", p.Deadline).
		add("amountraised", mp(p.PropertyDesired, raised)).
		add("tokensissued", mp(id, total)).
		add("issuerbonustokens", mp(id, issuerTokens)).
		add("addedissuertokens", mp(id, p.MissedTokens))
	if active == nil {
		o.add("closedearly", p.CloseEarly).add("maxtokens", p.MaxTokens)
	}
	if p.CloseEarly {
		o.add("endedtime", p.TimeClosed)
	}
	if p.CloseEarly && !p.MaxTokens {
		o.add("closetx", hash256(p.TxidClose))
	}
	if verbose {
		// std::map<std::string> order of "blocktime-txid".
		sortByKey(parts, func(p participant) string { return p.key })
		list := array{}
		for _, part := range parts {
			list = append(list, part.obj)
		}
		o.add("participanttransactions", list)
	}
	return o, nil
}

func getActiveCrowdsales(q *query, a args) (any, error) {
	crowds, err := q.st.Crowdsales()
	if err != nil {
		return nil, err
	}
	res := array{}
	for _, c := range crowds {
		p, err := q.st.Property(c.PropertyID)
		if err != nil {
			return nil, err
		}
		if p == nil {
			continue
		}
		start, err := q.creationTime(p.Txid)
		if err != nil {
			return nil, err
		}
		perUnit, err := q.formatMP(c.PropertyID, p.NumTokens)
		if err != nil {
			return nil, err
		}
		res = append(res, (&object{}).
			add("propertyid", c.PropertyID).
			add("name", string(p.Name)).
			add("issuer", p.Issuer).
			add("propertyiddesired", p.PropertyDesired).
			add("tokensperunit", perUnit).
			add("earlybonus", p.EarlyBird).
			add("percenttoissuer", p.Percentage).
			add("starttime", start).
			add("deadline", p.Deadline))
	}
	return res, nil
}

func getGrants(q *query, a args) (any, error) {
	if err := a.count(1, 1, "omni_getgrants propertyid"); err != nil {
		return nil, err
	}
	id, err := a.propertyID(0)
	if err != nil {
		return nil, err
	}
	p, err := q.requireExisting(id)
	if err != nil {
		return nil, err
	}
	if p.Fixed || !p.Manual {
		return nil, errorf(codeInvalidParam, "Property identifier does not refer to a managed property")
	}
	total, err := q.st.TotalTokens(id)
	if err != nil {
		return nil, err
	}
	issuances := array{}
	for _, h := range p.History {
		if h.Data[0] > 0 {
			s, _ := q.formatMP(id, h.Data[0])
			issuances = append(issuances, (&object{}).add("txid", h.Txid).add("grant", s))
		}
		if h.Data[1] > 0 {
			s, _ := q.formatMP(id, h.Data[1])
			issuances = append(issuances, (&object{}).add("txid", h.Txid).add("revoke", s))
		}
	}
	totalStr, err := q.formatMP(id, total)
	if err != nil {
		return nil, err
	}
	return (&object{}).
		add("propertyid", id).
		add("name", string(p.Name)).
		add("issuer", p.Issuer).
		add("creationtxid", hash256(p.Txid)).
		add("totaltokens", totalStr).
		add("issuances", issuances), nil
}

func getActiveDExSells(q *query, a args) (any, error) {
	if err := a.count(0, 1, "omni_getactivedexsells ( \"address\" )"); err != nil {
		return nil, err
	}
	filter := ""
	if a.has(0) {
		s, err := a.str(0)
		if err != nil {
			return nil, err
		}
		if s != "" {
			if filter, err = q.address(a, 0); err != nil {
				return nil, err
			}
		}
	}
	h, err := q.height()
	if err != nil {
		return nil, err
	}
	offers, err := q.st.Offers()
	if err != nil {
		return nil, err
	}
	accepts, err := q.st.Accepts()
	if err != nil {
		return nil, err
	}
	res := array{}
	for _, off := range offers {
		seller := off.Key
		for i := 0; i < len(seller); i++ {
			if seller[i] == '-' {
				seller = seller[:i]
				break
			}
		}
		if filter != "" && seller != filter {
			continue
		}
		id := off.Property
		t, err := q.st.Tally(seller, id)
		if err != nil {
			return nil, err
		}
		available, accepted := t[state.SellOfferReserve], t[state.AcceptReserve]
		div, err := q.st.Divisible(id)
		if err != nil {
			return nil, err
		}
		unitPrice := 0.0
		if off.AmountOriginal > 0 && off.LTCDesired > 0 {
			unitPrice = float64(off.LTCDesired) / float64(off.AmountOriginal)
			if !div {
				unitPrice /= 100000000.0
			}
		}
		mp := func(n int64) string {
			s, _ := q.formatMP(id, n)
			return s
		}
		matched := array{}
		for _, acc := range accepts {
			if acc.OfferTxid != off.Txid {
				continue
			}
			matched = append(matched, (&object{}).
				add("buyer", acc.Buyer).
				add("block", acc.Block).
				add("blocksleft", acc.Block+int64(off.BlockTimeLimit)-h).
				add("amount", mp(acc.AmountRemaining)).
				add("amounttopay", formatDivisible(state.DesiredLTC(acc.OfferAmountOriginal, acc.LTCDesiredOriginal, acc.AmountRemaining))))
		}
		res = append(res, (&object{}).
			add("txid", off.Txid).
			add("propertyid", id).
			add("seller", seller).
			add("amountavailable", mp(available)).
			add("litecoindesired", formatDivisible(state.DesiredLTC(off.AmountOriginal, off.LTCDesired, available))).
			add("unitprice", formatDivisible(int64(math.Round(math.Abs(unitPrice*100000000))))).
			add("timelimit", off.BlockTimeLimit).
			add("minimumfee", formatDivisible(off.MinFee)).
			add("amountaccepted", mp(accepted)).
			add("accepts", matched))
	}
	return res, nil
}

func listBlockTransactions(q *query, a args) (any, error) {
	if err := a.count(1, 1, "omni_listblocktransactions index"); err != nil {
		return nil, err
	}
	h, err := a.int64(0)
	if err != nil {
		return nil, err
	}
	tip, err := q.height()
	if err != nil {
		return nil, err
	}
	if h < 0 || h > tip {
		return nil, errorf(codeInvalidParam, "Block height is out of range")
	}
	return q.blockTxids(h, h)
}

func listBlocksTransactions(q *query, a args) (any, error) {
	if err := a.count(2, 2, "omni_listblockstransactions firstblock lastblock"); err != nil {
		return nil, err
	}
	first, err := a.int64(0)
	if err != nil {
		return nil, err
	}
	last, err := a.int64(1)
	if err != nil {
		return nil, err
	}
	txids, err := q.blockTxids(first, last)
	if err != nil {
		return nil, err
	}
	// GetOmniTxsInBlockRange collects into a std::set<uint256>.
	sortByKey(txids, func(v any) string { return reverseHex(v.(string)) })
	return txids, nil
}

func (q *query) blockTxids(first, last int64) (array, error) {
	res := array{}
	if first > last || last < 0 {
		return res, nil
	}
	kvs, err := q.snap.Scan(indexer.NsBlockTx, "")
	if err != nil {
		return nil, err
	}
	lo, hi := fmt.Sprintf("%010d:", max(first, 0)), fmt.Sprintf("%010d;", last)
	for _, kv := range kvs {
		if kv.Key >= lo && kv.Key < hi {
			res = append(res, string(kv.Value))
		}
	}
	return res, nil
}

func getActivations(q *query, a args) (any, error) {
	pending, completed, err := q.st.Activations()
	if err != nil {
		return nil, err
	}
	conv := func(list []state.Activation) array {
		out := array{}
		for _, act := range list {
			out = append(out, (&object{}).
				add("featureid", act.FeatureID).
				add("featurename", act.Name).
				add("activationblock", act.Block).
				add("minimumversion", act.MinClientVersion))
		}
		return out
	}
	return (&object{}).add("pendingactivations", conv(pending)).add("completedactivations", conv(completed)), nil
}

func getCurrentConsensusHash(q *query, a args) (any, error) {
	tip, err := q.snap.Tip()
	if err != nil {
		return nil, err
	}
	h, err := q.st.ConsensusHash()
	if err != nil {
		return nil, err
	}
	var height int64
	hash := hash256("")
	if tip != nil {
		height, hash = tip.Height, tip.Hash
	}
	return (&object{}).add("block", height).add("blockhash", hash).add("consensushash", h), nil
}

func (q *query) requireNonFungible(a args, i int) (uint32, error) {
	id, err := a.propertyID(i)
	if err != nil {
		return 0, err
	}
	p, err := q.requireExisting(id)
	if err != nil {
		return 0, err
	}
	if !p.Unique {
		return 0, errorf(codeInvalidParam, "Property identifier does not refer to a non-fungible property")
	}
	return id, nil
}

func getNonFungibleTokens(q *query, a args) (any, error) {
	if err := a.count(1, 2, "omni_getnonfungibletokens \"address\" ( propertyid )"); err != nil {
		return nil, err
	}
	addr, err := q.address(a, 0)
	if err != nil {
		return nil, err
	}
	var id uint32
	if a.has(1) {
		if id, err = q.requireNonFungible(a, 1); err != nil {
			return nil, err
		}
	}
	ranges, order, err := q.st.AddressNFTs(id, addr)
	if err != nil {
		return nil, err
	}
	res := array{}
	for _, pid := range order {
		tokens := array{}
		for _, r := range ranges[pid] {
			tokens = append(tokens, (&object{}).add("tokenstart", r.Start).add("tokenend", r.End).add("amount", r.End-r.Start+1))
		}
		res = append(res, (&object{}).add("propertyid", pid).add("tokens", tokens))
	}
	return res, nil
}

// maxTokenDataRange caps one omni_getnonfungibletokendata answer.
const maxTokenDataRange = 100000

func getNonFungibleTokenData(q *query, a args) (any, error) {
	if err := a.count(1, 3, "omni_getnonfungibletokendata propertyid ( tokenidstart tokenidend )"); err != nil {
		return nil, err
	}
	id, err := q.requireNonFungible(a, 0)
	if err != nil {
		return nil, err
	}
	ranges, err := q.st.NFTRanges(id, state.NFTOwners)
	if err != nil {
		return nil, err
	}
	if len(ranges) == 0 {
		return nil, nil
	}
	last := ranges[len(ranges)-1].End
	start, end := int64(1), last
	if a.has(1) {
		if start, err = a.int64(1); err != nil {
			return nil, err
		}
		start = min(max(start, 1), end)
		if !a.has(2) {
			end = start
		}
	}
	if a.has(2) {
		if end, err = a.int64(2); err != nil {
			return nil, err
		}
		end = min(max(end, start), last)
	}
	if end-start >= maxTokenDataRange {
		return nil, errorf(codeInvalidParam, fmt.Sprintf("Range too large, at most %d tokens per call", maxTokenDataRange))
	}
	res := array{}
	for tok := start; tok <= end; tok++ {
		o := (&object{}).add("index", tok)
		for _, f := range []struct {
			key string
			typ byte
		}{{"owner", state.NFTOwners}, {"grantdata", state.NFTGrantData}, {"issuerdata", state.NFTIssuerData}, {"holderdata", state.NFTHolderData}} {
			v, err := q.st.NFTValue(id, tok, f.typ)
			if err != nil {
				return nil, err
			}
			o.add(f.key, v)
		}
		res = append(res, o)
	}
	return res, nil
}

func getNonFungibleTokenRanges(q *query, a args) (any, error) {
	if err := a.count(1, 1, "omni_getnonfungibletokenranges propertyid"); err != nil {
		return nil, err
	}
	id, err := q.requireNonFungible(a, 0)
	if err != nil {
		return nil, err
	}
	ranges, err := q.st.NFTRanges(id, state.NFTOwners)
	if err != nil {
		return nil, err
	}
	res := array{}
	for _, r := range ranges {
		res = append(res, (&object{}).add("address", r.Value).add("tokenstart", r.Start).add("tokenend", r.End).add("amount", r.End-r.Start+1))
	}
	return res, nil
}

func createPayloadDExSell(q *query, a args) (any, error) {
	if err := a.count(6, 6, "omni_createpayload_dexsell propertyidforsale \"amountforsale\" \"amountdesired\" paymentwindow \"minacceptfee\" action"); err != nil {
		return nil, err
	}
	id, err := a.propertyID(0)
	if err != nil {
		return nil, err
	}
	act, err := a.int64(5)
	if err != nil {
		return nil, err
	}
	if act <= 0 || act > 3 {
		return nil, errorf(codeType, "Invalid action (1 = new, 2 = update, 3 = cancel only)")
	}
	var amount, desired, minFee int64
	var window int64
	if act <= 2 {
		div, err := q.st.Divisible(id)
		if err != nil {
			return nil, err
		}
		parse := func(i int, divisible bool) (int64, error) {
			s, err := a.str(i)
			if err != nil {
				return 0, err
			}
			n := strToInt64(s, divisible)
			if n < 1 {
				return 0, errorf(codeType, "Invalid amount")
			}
			return n, nil
		}
		if amount, err = parse(1, div); err != nil {
			return nil, err
		}
		if desired, err = parse(2, true); err != nil {
			return nil, err
		}
		if window, err = a.int64(3); err != nil {
			return nil, err
		}
		if window < 1 || window > 255 {
			return nil, errorf(codeType, "Payment window must be within 1-255 blocks")
		}
		s, err := a.str(4)
		if err != nil {
			return nil, err
		}
		if minFee = strToInt64(s, true); minFee < 0 {
			return nil, errorf(codeType, "Mininmum accept fee must be positive")
		}
	}
	p := make([]byte, 0, 34)
	p = binary.BigEndian.AppendUint16(p, 1)
	p = binary.BigEndian.AppendUint16(p, 20)
	p = binary.BigEndian.AppendUint32(p, id)
	p = binary.BigEndian.AppendUint64(p, uint64(amount))
	p = binary.BigEndian.AppendUint64(p, uint64(desired))
	p = append(p, byte(window))
	p = binary.BigEndian.AppendUint64(p, uint64(minFee))
	p = append(p, byte(act))
	return hex.EncodeToString(p), nil
}
