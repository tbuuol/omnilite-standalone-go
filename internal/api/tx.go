package api

import (
	"encoding/hex"
	"encoding/json"
	"errors"
	"sort"

	"github.com/tbuuol/omnilite-standalone/internal/indexer"
	"github.com/tbuuol/omnilite-standalone/internal/omni"
	"github.com/tbuuol/omnilite-standalone/internal/state"
)

func sortByKey[T any](list []T, key func(T) string) {
	sort.SliceStable(list, func(i, j int) bool { return key(list[i]) < key(list[j]) })
}

// reverseHex turns a txid into uint256's internal byte order, which
// std::set<uint256> sorts by.
func reverseHex(h string) string {
	b := []byte(h)
	out := make([]byte, 0, len(b))
	for i := len(b) - 2; i >= 0; i -= 2 {
		out = append(out, b[i], b[i+1])
	}
	return string(out)
}

func (q *query) record(txid string) (*indexer.Record, error) {
	v, ok, err := q.snap.Get(indexer.NsTx, txid)
	if err != nil || !ok {
		return nil, err
	}
	var r indexer.Record
	return &r, json.Unmarshal(v, &r)
}

func getTransaction(q *query, a args) (any, error) {
	if err := a.count(1, 1, "omni_gettransaction \"txid\""); err != nil {
		return nil, err
	}
	txid, err := a.txid(0)
	if err != nil {
		return nil, err
	}
	return q.transaction(txid, false, "")
}

func getSTO(q *query, a args) (any, error) {
	if err := a.count(1, 2, "omni_getsto \"txid\" \"recipientfilter\""); err != nil {
		return nil, err
	}
	txid, err := a.txid(0)
	if err != nil {
		return nil, err
	}
	filter := ""
	if a.has(1) {
		s, err := a.str(1)
		if err != nil {
			return nil, err
		}
		if filter = s; s != "*" {
			if filter, err = q.address(a, 1); err != nil {
				return nil, err
			}
		}
	}
	return q.transaction(txid, true, filter)
}

// showReference mirrors showRefForTx.
func showReference(t uint16) bool {
	switch t {
	case omni.TypeSendToOwners, omni.TypeTradeOffer, omni.TypeCreatePropertyFixed, omni.TypeCreatePropertyVar,
		omni.TypeCreatePropertyMan, omni.TypeCloseCrowdsale, omni.TypeRevokeTokens, omni.TypeEnableFreezing,
		omni.TypeDisableFreezing, omni.TypeActivation:
		return false
	}
	return true
}

// transaction mirrors populateRPCTransactionObject. Unconfirmed
// transactions come from the mempool and have no validity yet.
func (q *query) transaction(txid string, extended bool, filter string) (any, error) {
	rec, err := q.record(txid)
	if err != nil {
		return nil, err
	}
	if rec == nil {
		if q.pool != nil {
			if e := q.pool.Get(txid); e != nil {
				return q.unconfirmed(e.Record, filter)
			}
		}
		return nil, errorf(codeInvalidAddress, "No Omni Layer Protocol transaction")
	}
	tip, err := q.height()
	if err != nil {
		return nil, err
	}
	blk, err := q.snap.BlockAt(rec.Block)
	if err != nil {
		return nil, err
	}
	blockHash := hash256("")
	if blk != nil {
		blockHash = blk.Hash
	}
	confirmations := 1 + tip - rec.Block
	res, err := q.st.Result(txid)
	if err != nil {
		return nil, err
	}

	if rec.Kind == indexer.KindDExPayment {
		if res == nil || len(res.Payments) == 0 {
			return nil, errorf(codeInternal, "Generic transaction population failure")
		}
		paid := map[int]int64{}
		for _, p := range rec.Payments {
			paid[p.Vout] = p.Value
		}
		purchases := array{}
		for _, p := range res.Payments {
			if filter != "" && filter != "*" && rec.Sender != filter && p.Seller != filter {
				continue
			}
			bought, err := q.formatMP(p.Property, p.Amount)
			if err != nil {
				return nil, err
			}
			purchases = append(purchases, (&object{}).
				add("vout", p.Vout).
				add("amountpaid", formatDivisible(paid[p.Vout])).
				add("ismine", false).
				add("referenceaddress", p.Seller).
				add("propertyid", p.Property).
				add("amountbought", bought).
				add("valid", true))
		}
		if len(purchases) == 0 {
			return nil, errorf(codeInternal, "Generic transaction population failure")
		}
		return (&object{}).
			add("txid", txid).
			add("type", "DEx Purchase").
			add("sendingaddress", rec.Sender).
			add("purchases", purchases).
			add("blockhash", blockHash).
			add("blocktime", rec.Time).
			add("block", rec.Block).
			add("confirmations", confirmations), nil
	}

	payload, err := hex.DecodeString(rec.Payload)
	if err != nil {
		return nil, err
	}
	m, err := omni.Interpret(payload, rec.Sender, rec.Reference, q.params)
	if err != nil {
		return nil, errorf(codeInvalidAddress, "No Omni Layer Protocol transaction")
	}
	valid := res != nil && res.Valid
	o := (&object{}).
		add("txid", txid).
		add("fee", formatDivisible(rec.Fee)).
		add("sendingaddress", rec.Sender)
	if showReference(m.Type) {
		o.add("referenceaddress", m.Receiver)
	}
	o.add("ismine", false).add("version", m.Version).add("type_int", m.Type)
	if m.Type != omni.TypeSimpleSend {
		o.add("type", omni.TypeName(m.Type))
	}
	if err := q.typeInfo(o, txid, m, res, true, extended, filter); err != nil {
		return nil, err
	}
	o.add("valid", valid)
	if !valid {
		code := -999999
		if res != nil {
			code = res.Code
		}
		o.add("invalidreason", errorString(code))
	}
	return o.add("blockhash", blockHash).
		add("blocktime", rec.Time).
		add("positioninblock", rec.Index).
		add("block", rec.Block).
		add("confirmations", confirmations), nil
}

// typeInfo mirrors populateRPCTypeInfo.
func (q *query) typeInfo(o *object, txid string, m *omni.Message, res *state.Result, confirmed, extended bool, filter string) error {
	mp := func(id uint32, n uint64) string {
		s, _ := q.formatMP(id, int64(n))
		return s
	}
	div := func(id uint32) bool {
		d, _ := q.st.Divisible(id)
		return d
	}
	valid := res != nil && res.Valid
	var newAmount uint64
	if res != nil {
		newAmount = res.NewAmount
	}
	createdProperty := func() error {
		if !confirmed {
			return nil
		}
		id, err := q.st.PropertyByTx(txid)
		if err != nil {
			return err
		}
		if id > 0 {
			o.add("propertyid", id).add("divisible", div(id))
		}
		return nil
	}
	spFields := func() {
		o.add("category", m.Category).add("subcategory", m.Subcategory).add("propertyname", m.Name).
			add("data", m.Data).add("url", m.URL)
	}

	switch m.Type {
	case omni.TypeSimpleSend:
		if res != nil && res.CrowdProperty != 0 {
			p, err := q.st.Property(res.CrowdProperty)
			if err != nil || p == nil {
				return err
			}
			o.add("type", "Crowdsale Purchase").
				add("propertyid", m.Property).add("divisible", div(m.Property)).add("amount", mp(m.Property, m.Amount)).
				add("purchasedpropertyid", res.CrowdProperty).
				add("purchasedpropertyname", string(p.Name)).
				add("purchasedpropertydivisible", p.Divisible()).
				add("purchasedtokens", mp(res.CrowdProperty, uint64(res.CrowdTokens))).
				add("issuertokens", mp(res.CrowdProperty, uint64(res.CrowdIssuer)))
			return nil
		}
		o.add("type", "Simple Send").add("propertyid", m.Property).add("divisible", div(m.Property)).add("amount", mp(m.Property, m.Amount))
	case omni.TypeSendToOwners:
		o.add("propertyid", m.Property).add("divisible", div(m.Property)).add("amount", mp(m.Property, m.Amount))
		if extended {
			q.stoDetails(o, m, res, filter)
		}
	case omni.TypeSendAll:
		switch m.Ecosystem {
		case 1:
			o.add("ecosystem", "main")
		case 2:
			o.add("ecosystem", "test")
		default:
			o.add("ecosystem", "all")
		}
		if res != nil && len(res.SendAll) > 0 {
			subs := array{}
			for _, s := range res.SendAll {
				subs = append(subs, (&object{}).add("propertyid", s.Property).add("divisible", div(s.Property)).add("amount", mp(s.Property, uint64(s.Amount))))
			}
			o.add("subsends", subs)
		}
	case omni.TypeTradeOffer:
		amount, desired := int64(m.Amount), int64(m.AmountDesired)
		sub := m.Subaction
		if m.Version == 0 {
			sub = 0
		}
		if sub > 3 {
			sub = 0
		}
		if sub == 0 && amount > 0 {
			sub = 1
		}
		if sub == 0 && amount == 0 {
			sub = 3
		}
		if valid && newAmount > 0 {
			desired = state.DesiredLTC(amount, desired, int64(newAmount))
			amount = int64(newAmount)
		}
		o.add("propertyid", m.Property).add("divisible", div(m.Property)).add("amount", mp(m.Property, uint64(amount))).
			add("litecoindesired", formatDivisible(desired)).
			add("timelimit", m.BlockTimeLimit).
			add("feerequired", formatDivisible(int64(m.MinFee)))
		switch sub {
		case 1:
			o.add("action", "new")
		case 2:
			o.add("action", "update")
		case 3:
			o.add("action", "cancel")
		}
	case omni.TypeAcceptOffer:
		amount := m.Amount
		if valid && newAmount > 0 {
			amount = newAmount
		}
		o.add("propertyid", m.Property).add("divisible", div(m.Property)).add("amount", mp(m.Property, amount))
	case omni.TypeCreatePropertyFixed:
		if err := createdProperty(); err != nil {
			return err
		}
		o.add("ecosystem", ecosystemName(m.Ecosystem)).add("propertytype", propertyTypeName(m.PropType))
		spFields()
		o.add("amount", formatByType(int64(m.Amount), m.PropType))
	case omni.TypeCreatePropertyVar:
		if err := createdProperty(); err != nil {
			return err
		}
		o.add("propertytype", propertyTypeName(m.PropType)).add("ecosystem", ecosystemName(m.Ecosystem))
		spFields()
		o.add("propertyiddesired", m.Property).
			add("tokensperunit", mp(m.Property, m.Amount)).
			add("deadline", m.Deadline).
			add("earlybonus", m.EarlyBird).
			add("percenttoissuer", m.Percentage).
			add("amount", formatByType(0, m.PropType))
	case omni.TypeCreatePropertyMan:
		if err := createdProperty(); err != nil {
			return err
		}
		o.add("propertytype", propertyTypeName(m.PropType)).add("ecosystem", ecosystemName(m.Ecosystem))
		spFields()
		o.add("amount", formatByType(0, m.PropType))
	case omni.TypeCloseCrowdsale, omni.TypeChangeIssuer:
		o.add("propertyid", m.Property).add("divisible", div(m.Property))
	case omni.TypeGrantTokens:
		o.add("propertyid", m.Property).add("divisible", div(m.Property)).add("amount", mp(m.Property, m.Amount))
		p, err := q.st.Property(m.Property)
		if err != nil || p == nil {
			return err
		}
		if p.Unique {
			var start, end int64
			if res != nil {
				start, end = res.GrantStart, res.GrantEnd
			}
			o.add("tokenstart", formatIndivisible(start)).add("tokenend", formatIndivisible(end)).add("grantdata", m.NFTData)
		}
	case omni.TypeRevokeTokens:
		o.add("propertyid", m.Property).add("divisible", div(m.Property)).add("amount", mp(m.Property, m.Amount))
	case omni.TypeSendNonFungible:
		o.add("propertyid", m.Property).add("tokenstart", int64(m.TokenStart)).add("tokenend", int64(m.TokenEnd)).
			add("amount", int64(m.TokenEnd)-int64(m.TokenStart)+1)
	case omni.TypeNonFungibleData:
		issuer := "false"
		if m.NFTDataType == 1 {
			issuer = "true"
		}
		o.add("propertyid", m.Property).add("tokenstart", int64(m.TokenStart)).add("tokenend", int64(m.TokenEnd)).
			add("issuer", issuer).add("data", m.NFTData)
	case omni.TypeActivation:
		o.add("featureid", m.FeatureID).add("activationblock", m.ActivationBlock).add("minimumversion", m.MinClientVersion)
	case omni.TypeEnableFreezing, omni.TypeDisableFreezing, omni.TypeFreezeTokens, omni.TypeUnfreezeTokens:
		o.add("propertyid", m.Property)
	case omni.TypeAnyData:
		// getPayloadData: the payload after version and type.
		v, _, err := q.snap.Get(indexer.NsTx, txid)
		if err != nil {
			return err
		}
		var rec indexer.Record
		if err := json.Unmarshal(v, &rec); err != nil {
			return err
		}
		data := ""
		if len(rec.Payload) > 8 {
			data = rec.Payload[8:]
		}
		o.add("data", data)
	}
	return nil
}

// stoDetails mirrors populateRPCExtendedTypeSendToOwners. Without a
// wallet, an empty filter matches no recipient; "*" matches all.
func (q *query) stoDetails(o *object, m *omni.Message, res *state.Result, filter string) {
	var receivers []state.STOReceive
	if res != nil {
		receivers = append(receivers, res.STO...)
	}
	sortByKey(receivers, func(r state.STOReceive) string { return r.Address })
	fee := int64(1)
	if m.Version > 0 {
		fee = 1000
	}
	list := array{}
	for _, r := range receivers {
		if filter == "" || (filter != "*" && filter != r.Address) {
			continue
		}
		s, _ := q.formatMP(m.Property, r.Amount)
		list = append(list, (&object{}).add("address", r.Address).add("amount", s))
	}
	o.add("totalstofee", formatDivisible(fee*int64(len(receivers)))).add("recipients", list)
}

func ecosystemName(e uint8) string {
	switch e {
	case 1:
		return "main"
	case 2:
		return "test"
	}
	return "unknown"
}

func propertyTypeName(t uint16) string {
	switch t {
	case state.PropTypeDivisible:
		return "divisible"
	case state.PropTypeIndivisible:
		return "indivisible"
	case state.PropTypeNonFungible:
		return "non-fungible"
	}
	return "unknown"
}

// unconfirmed populates a mempool transaction: no validity, no block.
func (q *query) unconfirmed(rec *indexer.Record, filter string) (any, error) {
	payload, err := hex.DecodeString(rec.Payload)
	if err != nil {
		return nil, err
	}
	m, err := omni.Interpret(payload, rec.Sender, rec.Reference, q.params)
	if err != nil {
		return nil, errorf(codeInvalidAddress, "No Omni Layer Protocol transaction")
	}
	if filter != "" && rec.Sender != filter && m.Receiver != filter {
		return nil, errorf(codeInternal, "Generic transaction population failure")
	}
	o := (&object{}).
		add("txid", rec.Txid).
		add("fee", formatDivisible(rec.Fee)).
		add("sendingaddress", rec.Sender)
	if showReference(m.Type) {
		o.add("referenceaddress", m.Receiver)
	}
	o.add("ismine", false).add("version", m.Version).add("type_int", m.Type)
	if m.Type != omni.TypeSimpleSend {
		o.add("type", omni.TypeName(m.Type))
	}
	if err := q.typeInfo(o, rec.Txid, m, nil, false, false, ""); err != nil {
		return nil, err
	}
	return o.add("confirmations", 0), nil
}

func listPendingTransactions(q *query, a args) (any, error) {
	if err := a.count(0, 1, "omni_listpendingtransactions ( \"address\" )"); err != nil {
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
	res := array{}
	if q.pool == nil {
		return res, nil
	}
	for _, e := range q.pool.List() {
		// Confirmed meanwhile; the next mempool poll drops it.
		if rec, err := q.record(e.Record.Txid); err != nil {
			return nil, err
		} else if rec != nil {
			continue
		}
		obj, err := q.unconfirmed(e.Record, filter)
		var apiErr *Error
		if errors.As(err, &apiErr) {
			continue
		}
		if err != nil {
			return nil, err
		}
		res = append(res, obj)
	}
	return res, nil
}
