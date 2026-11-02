package api

import (
	"context"
	"encoding/hex"
	"encoding/json"

	"github.com/tbuuol/omnilite-standalone/internal/omni"
	"github.com/tbuuol/omnilite-standalone/internal/state"
	"github.com/tbuuol/omnilite-standalone/internal/wallet"
)

// Token management: issuance, grants, crowdsales, freezing, STO and NFT
// data (rpctx.cpp, rpcpayload.cpp).

// registerManage adds the calls of this file; wallet.go's init runs it once the
// method tables exist.
func registerManage() {
	for name, h := range map[string]nodeHandler{
		"omni_sendissuancefixed":     sendIssuanceFixed,
		"omni_sendissuancecrowdsale": sendIssuanceCrowdsale,
		"omni_sendissuancemanaged":   sendIssuanceManaged,
		"omni_sendsto":               sendSTO,
		"omni_sendgrant":             sendGrant,
		"omni_sendrevoke":            sendRevoke,
		"omni_sendclosecrowdsale":    sendCloseCrowdsale,
		"omni_sendchangeissuer":      sendChangeIssuer,
		"omni_sendenablefreezing":    sendEnableFreezing,
		"omni_senddisablefreezing":   sendDisableFreezing,
		"omni_sendfreeze":            sendFreeze,
		"omni_sendunfreeze":          sendUnfreeze,
		"omni_setnonfungibledata":    setNonFungibleData,
	} {
		nodeMethods[name] = h
		walletMethods[name] = true
	}
	for name, h := range map[string]handler{
		"omni_createpayload_issuancefixed":      createPayloadIssuanceFixed,
		"omni_createpayload_issuancecrowdsale":  createPayloadIssuanceCrowdsale,
		"omni_createpayload_issuancemanaged":    createPayloadIssuanceManaged,
		"omni_createpayload_sto":                createPayloadSTO,
		"omni_createpayload_grant":              createPayloadGrant,
		"omni_createpayload_revoke":             createPayloadRevoke,
		"omni_createpayload_closecrowdsale":     createPayloadPropertyOnly(omni.PayloadCloseCrowdsale, "closecrowdsale"),
		"omni_createpayload_changeissuer":       createPayloadPropertyOnly(omni.PayloadChangeIssuer, "changeissuer"),
		"omni_createpayload_enablefreezing":     createPayloadPropertyOnly(omni.PayloadEnableFreezing, "enablefreezing"),
		"omni_createpayload_disablefreezing":    createPayloadPropertyOnly(omni.PayloadDisableFreezing, "disablefreezing"),
		"omni_createpayload_freeze":             createPayloadFreeze(true),
		"omni_createpayload_unfreeze":           createPayloadFreeze(false),
		"omni_createpayload_setnonfungibledata": createPayloadSetNonFungibleData,
	} {
		methods[name] = h
	}
}

// --- argument parsing (rpcvalues.cpp) ---

// text mirrors ParseText.
func (a args) text(i int) (string, error) {
	s, err := a.str(i)
	if err != nil {
		return "", err
	}
	if len(s) > 255 {
		return "", errorf(codeInvalidParam, "Text must not be longer than 255 characters")
	}
	return s, nil
}

// optText is the text at i, or "" if there is no parameter i.
func (a args) optText(i int) (string, error) {
	if len(a) <= i {
		return "", nil
	}
	return a.text(i)
}

// ranged parses an integer and fails with msg outside [lo, hi].
func (a args) ranged(i int, lo, hi int64, code int, msg string) (int64, error) {
	n, err := a.int64(i)
	if err != nil {
		return 0, err
	}
	if n < lo || n > hi {
		return 0, errorf(code, msg)
	}
	return n, nil
}

// amountForType mirrors ParseAmount(value, propertyType): only type 2 is
// divisible.
func (a args) amountForType(i int, typ uint16) (int64, error) { return a.amount(i, typ == 2) }

// propertyInfo parses ecosystem, type, previous id and the five texts
// starting at i (ParseEcosystem, ParsePropertyType or
// ParseManagedPropertyType, ParsePreviousPropertyId, ParseText).
func (a args) propertyInfo(i int, managed bool) (*omni.PropertyInfo, error) {
	eco, err := a.ecosystem(i)
	if err != nil {
		return nil, err
	}
	var typ int64
	if managed {
		if typ, err = a.int64(i + 1); err == nil && typ != 1 && typ != 2 && typ != 5 {
			err = errorf(codeInvalidParam, "Invalid property type (1 = indivisible, 2 = divisible, 5 = non-fungible only)")
		}
	} else {
		typ, err = a.ranged(i+1, 1, 2, codeInvalidParam, "Invalid property type (1 = indivisible, 2 = divisible only)")
	}
	if err != nil {
		return nil, err
	}
	if _, err := a.ranged(i+2, 0, 0, codeType, "Property appends/replaces are not yet supported"); err != nil {
		return nil, err
	}
	info := &omni.PropertyInfo{Ecosystem: eco, Type: uint16(typ)}
	for k, dst := range []*string{&info.Category, &info.Subcategory, &info.Name, &info.URL, &info.Data} {
		if *dst, err = a.text(i + 3 + k); err != nil {
			return nil, err
		}
	}
	// RequirePropertyName
	if info.Name == "" {
		return nil, errorf(codeType, "Property name must not be empty")
	}
	return info, nil
}

// crowdsaleTerms parses desired property, tokens per unit, deadline and
// both bonuses starting at i, for a crowdsale of property type typ.
func (a args) crowdsaleTerms(i int, typ uint16) (desired uint32, perUnit, deadline, early, issuer int64, err error) {
	if desired, err = a.propertyID(i); err != nil {
		return
	}
	if perUnit, err = a.amountForType(i+1, typ); err != nil {
		return
	}
	if deadline, err = a.int64(i + 2); err == nil && deadline < 0 {
		err = errorf(codeType, "Deadline must be positive")
	}
	if err != nil {
		return
	}
	if early, err = a.ranged(i+3, 0, 255, codeType, "Early bird bonus must be in the range of 0-255 percent per week"); err != nil {
		return
	}
	issuer, err = a.ranged(i+4, 0, 255, codeType, "Bonus for issuer must be in the range of 0-255 percent")
	return
}

// issuerFlag reads the issuer parameter of NFT data: a number or a boolean.
func (a args) issuerFlag(i int) (bool, error) {
	var n json.Number
	if err := json.Unmarshal(a[i], &n); err == nil {
		v, err := n.Int64()
		if err != nil {
			return false, errorf(codeMisc, "JSON integer out of range")
		}
		return v != 0, nil
	}
	return a.boolean(i)
}

// --- requirements (rpcrequirements.cpp) ---

func isTestEcosystem(id uint32) bool { return id == 2 || id >= 0x80000003 }

// requireSameEcosystem mirrors RequireSameEcosystem as the issuance calls
// use it: with the ecosystem number (1 or 2) standing in for a property.
func requireSameEcosystem(ecosystem uint8, other uint32) error {
	if isTestEcosystem(uint32(ecosystem)) != isTestEcosystem(other) {
		return errorf(codeInvalidParam, "Properties must be in the same ecosystem")
	}
	return nil
}

func (q *query) storedProperty(id uint32) (*state.Property, error) {
	p, err := q.st.Property(id)
	if err != nil {
		return nil, err
	}
	if p == nil {
		return nil, errorf(codeDatabase, "Failed to retrieve property")
	}
	return p, nil
}

func (q *query) requireManaged(id uint32) error {
	p, err := q.storedProperty(id)
	if err != nil {
		return err
	}
	if p.Fixed || !p.Manual {
		return errorf(codeInvalidParam, "Property identifier does not refer to a managed property")
	}
	return nil
}

func (q *query) requireTokenIssuer(addr string, id uint32) error {
	p, err := q.storedProperty(id)
	if err != nil {
		return err
	}
	if addr != p.Issuer {
		return errorf(codeType, "Sender is not authorized to manage the property")
	}
	return nil
}

func (q *query) requireCrowdsale(id uint32) error {
	p, err := q.storedProperty(id)
	if err != nil {
		return err
	}
	if p.Fixed || p.Manual {
		return errorf(codeInvalidParam, "Property identifier does not refer to a crowdsale")
	}
	return nil
}

func (q *query) requireActiveCrowdsale(id uint32) error {
	list, err := q.st.Crowdsales()
	if err != nil {
		return err
	}
	for _, c := range list {
		if c.PropertyID == id {
			return nil
		}
	}
	return errorf(codeType, "Property identifier does not refer to an active crowdsale")
}

func (q *query) requireNonFungibleProperty(id uint32) error {
	p, err := q.storedProperty(id)
	if err != nil {
		return err
	}
	if !p.Unique {
		return errorf(codeInvalidParam, "Property identifier does not refer to a non-fungible property")
	}
	return nil
}

// --- payloads ---

func payloadHex(p []byte) (any, error) { return hex.EncodeToString(p), nil }

func createPayloadIssuanceFixed(q *query, a args) (any, error) {
	if err := a.count(9, 9, "omni_createpayload_issuancefixed ecosystem type previousid \"category\" \"subcategory\" \"name\" \"url\" \"data\" \"amount\""); err != nil {
		return nil, err
	}
	info, err := a.propertyInfo(0, false)
	if err != nil {
		return nil, err
	}
	amount, err := a.amountForType(8, info.Type)
	if err != nil {
		return nil, err
	}
	return payloadHex(omni.PayloadIssuanceFixed(info, uint64(amount)))
}

func createPayloadIssuanceCrowdsale(q *query, a args) (any, error) {
	if err := a.count(13, 13, "omni_createpayload_issuancecrowdsale ecosystem type previousid \"category\" \"subcategory\" \"name\" \"url\" \"data\" propertyiddesired tokensperunit deadline earlybonus issuerpercentage"); err != nil {
		return nil, err
	}
	info, err := a.propertyInfo(0, false)
	if err != nil {
		return nil, err
	}
	desired, perUnit, deadline, early, issuer, err := a.crowdsaleTerms(8, info.Type)
	if err != nil {
		return nil, err
	}
	if err := requireSameEcosystem(info.Ecosystem, desired); err != nil {
		return nil, err
	}
	return payloadHex(omni.PayloadIssuanceCrowdsale(info, desired, uint64(perUnit), uint64(deadline), uint8(early), uint8(issuer)))
}

func createPayloadIssuanceManaged(q *query, a args) (any, error) {
	if err := a.count(8, 8, "omni_createpayload_issuancemanaged ecosystem type previousid \"category\" \"subcategory\" \"name\" \"url\" \"data\""); err != nil {
		return nil, err
	}
	info, err := a.propertyInfo(0, true)
	if err != nil {
		return nil, err
	}
	return payloadHex(omni.PayloadIssuanceManaged(info))
}

func createPayloadSTO(q *query, a args) (any, error) {
	if err := a.count(2, 3, "omni_createpayload_sto propertyid \"amount\" ( distributionproperty )"); err != nil {
		return nil, err
	}
	id, amount, err := q.propertyAndAmount(a, 0)
	if err != nil {
		return nil, err
	}
	dist := id
	if len(a) > 2 {
		if dist, err = a.propertyID(2); err != nil {
			return nil, err
		}
	}
	return payloadHex(omni.PayloadSendToOwners(id, uint64(amount), dist))
}

// propertyAndAmount parses a property id at i and an amount in its unit
// at i+1.
func (q *query) propertyAndAmount(a args, i int) (uint32, int64, error) {
	id, err := a.propertyID(i)
	if err != nil {
		return 0, 0, err
	}
	div, err := q.st.Divisible(id)
	if err != nil {
		return 0, 0, err
	}
	amount, err := a.amount(i+1, div)
	return id, amount, err
}

func createPayloadGrant(q *query, a args) (any, error) {
	// OmniLite's help says the memo is optional, but it requires all three.
	if err := a.count(3, 3, "omni_createpayload_grant propertyid \"amount\" ( \"memo\" )"); err != nil {
		return nil, err
	}
	id, amount, err := q.propertyAndAmount(a, 0)
	if err != nil {
		return nil, err
	}
	memo, err := a.optText(2)
	if err != nil {
		return nil, err
	}
	return payloadHex(omni.PayloadGrant(id, uint64(amount), memo))
}

func createPayloadRevoke(q *query, a args) (any, error) {
	if err := a.count(2, 3, "omni_createpayload_revoke propertyid \"amount\" ( \"memo\" )"); err != nil {
		return nil, err
	}
	id, amount, err := q.propertyAndAmount(a, 0)
	if err != nil {
		return nil, err
	}
	memo, err := a.optText(2)
	if err != nil {
		return nil, err
	}
	return payloadHex(omni.PayloadRevoke(id, uint64(amount), memo))
}

func createPayloadPropertyOnly(build func(uint32) []byte, name string) handler {
	return func(q *query, a args) (any, error) {
		if err := a.count(1, 1, "omni_createpayload_"+name+" propertyid"); err != nil {
			return nil, err
		}
		id, err := a.propertyID(0)
		if err != nil {
			return nil, err
		}
		return payloadHex(build(id))
	}
}

func createPayloadFreeze(freeze bool) handler {
	name := map[bool]string{true: "freeze", false: "unfreeze"}[freeze]
	return func(q *query, a args) (any, error) {
		if err := a.count(3, 3, "omni_createpayload_"+name+" \"toaddress\" propertyid amount"); err != nil {
			return nil, err
		}
		target, err := q.address(a, 0)
		if err != nil {
			return nil, err
		}
		id, amount, err := q.propertyAndAmount(a, 1)
		if err != nil {
			return nil, err
		}
		d, err := omni.DecodeAddress(target, q.params)
		if err != nil {
			return nil, err
		}
		return payloadHex(omni.PayloadFreeze(freeze, id, uint64(amount), d, q.params))
	}
}

// nftDataArgs parses property, range, issuer flag and data.
func (a args) nftDataArgs() (id uint32, start, end int64, issuer bool, data string, err error) {
	if id, err = a.propertyID(0); err != nil {
		return
	}
	if start, err = a.int64(1); err != nil {
		return
	}
	if end, err = a.int64(2); err != nil {
		return
	}
	if issuer, err = a.issuerFlag(3); err != nil {
		return
	}
	data, err = a.text(4)
	return
}

func createPayloadSetNonFungibleData(q *query, a args) (any, error) {
	if err := a.count(5, 5, "omni_createpayload_setnonfungibledata propertyid tokenstart tokenend issuer \"data\""); err != nil {
		return nil, err
	}
	id, start, end, issuer, data, err := a.nftDataArgs()
	if err != nil {
		return nil, err
	}
	if err := requireSaneNonFungibleRange(start, end); err != nil {
		return nil, err
	}
	return payloadHex(omni.PayloadSetNonFungibleData(id, uint64(start), uint64(end), issuer, data))
}

// --- sending ---

// sendWith parses and checks inside a snapshot, then sends.
func (s *Server) sendWith(ctx context.Context, prepare func(q *query, req *wallet.Request) error) (any, error) {
	req := &wallet.Request{}
	if err := s.read(ctx, func(q *query) error { return prepare(q, req) }); err != nil {
		return nil, err
	}
	return s.commit(ctx, req)
}

func sendIssuanceFixed(ctx context.Context, s *Server, a args) (any, error) {
	if err := a.count(10, 10, "omni_sendissuancefixed \"fromaddress\" ecosystem type previousid \"category\" \"subcategory\" \"name\" \"url\" \"data\" \"amount\""); err != nil {
		return nil, err
	}
	return s.sendWith(ctx, func(q *query, req *wallet.Request) error {
		var err error
		if req.From, err = q.address(a, 0); err != nil {
			return err
		}
		info, err := a.propertyInfo(1, false)
		if err != nil {
			return err
		}
		amount, err := a.amountForType(9, info.Type)
		if err != nil {
			return err
		}
		req.Payload = omni.PayloadIssuanceFixed(info, uint64(amount))
		return nil
	})
}

func sendIssuanceCrowdsale(ctx context.Context, s *Server, a args) (any, error) {
	if err := a.count(14, 14, "omni_sendissuancecrowdsale \"fromaddress\" ecosystem type previousid \"category\" \"subcategory\" \"name\" \"url\" \"data\" propertyiddesired tokensperunit deadline earlybonus issuerpercentage"); err != nil {
		return nil, err
	}
	return s.sendWith(ctx, func(q *query, req *wallet.Request) error {
		var err error
		if req.From, err = q.address(a, 0); err != nil {
			return err
		}
		info, err := a.propertyInfo(1, false)
		if err != nil {
			return err
		}
		desired, perUnit, deadline, early, issuer, err := a.crowdsaleTerms(9, info.Type)
		if err != nil {
			return err
		}
		if _, err := q.requireExisting(desired); err != nil {
			return err
		}
		if err := requireSameEcosystem(info.Ecosystem, desired); err != nil {
			return err
		}
		req.Payload = omni.PayloadIssuanceCrowdsale(info, desired, uint64(perUnit), uint64(deadline), uint8(early), uint8(issuer))
		return nil
	})
}

func sendIssuanceManaged(ctx context.Context, s *Server, a args) (any, error) {
	if err := a.count(9, 9, "omni_sendissuancemanaged \"fromaddress\" ecosystem type previousid \"category\" \"subcategory\" \"name\" \"url\" \"data\""); err != nil {
		return nil, err
	}
	return s.sendWith(ctx, func(q *query, req *wallet.Request) error {
		var err error
		if req.From, err = q.address(a, 0); err != nil {
			return err
		}
		info, err := a.propertyInfo(1, true)
		if err != nil {
			return err
		}
		req.Payload = omni.PayloadIssuanceManaged(info)
		return nil
	})
}

func sendSTO(ctx context.Context, s *Server, a args) (any, error) {
	if err := a.count(3, 5, "omni_sendsto \"fromaddress\" propertyid \"amount\" ( \"redeemaddress\" distributionproperty )"); err != nil {
		return nil, err
	}
	return s.sendWith(ctx, func(q *query, req *wallet.Request) error {
		var err error
		if req.From, err = q.address(a, 0); err != nil {
			return err
		}
		if req.Property, req.Amount, err = q.propertyAndAmount(a, 1); err != nil {
			return err
		}
		if r, err := a.optString(3); err != nil {
			return err
		} else if r != "" {
			if req.Redeem, err = q.address(a, 3); err != nil {
				return err
			}
		}
		dist := req.Property
		if len(a) > 4 {
			if dist, err = a.propertyID(4); err != nil {
				return err
			}
		}
		if err := q.requireBalance(req.From, req.Property, req.Amount); err != nil {
			return err
		}
		req.Payload = omni.PayloadSendToOwners(req.Property, uint64(req.Amount), dist)
		return nil
	})
}

func sendGrant(ctx context.Context, s *Server, a args) (any, error) {
	if err := a.count(4, 5, "omni_sendgrant \"fromaddress\" \"toaddress\" propertyid \"amount\" ( \"memo\" )"); err != nil {
		return nil, err
	}
	return s.sendWith(ctx, func(q *query, req *wallet.Request) error {
		var err error
		if req.From, err = q.address(a, 0); err != nil {
			return err
		}
		if to, err := a.text(1); err != nil {
			return err
		} else if to != "" {
			if req.To, err = q.address(a, 1); err != nil {
				return err
			}
		}
		id, amount, err := q.propertyAndAmount(a, 2)
		if err != nil {
			return err
		}
		memo, err := a.optText(4)
		if err != nil {
			return err
		}
		if err := q.requireManagedByIssuer(req.From, id); err != nil {
			return err
		}
		req.Payload = omni.PayloadGrant(id, uint64(amount), memo)
		return nil
	})
}

// requireManagedByIssuer is RequireExistingProperty, RequireManagedProperty
// and RequireTokenIssuer.
func (q *query) requireManagedByIssuer(addr string, id uint32) error {
	if _, err := q.requireExisting(id); err != nil {
		return err
	}
	if err := q.requireManaged(id); err != nil {
		return err
	}
	return q.requireTokenIssuer(addr, id)
}

func sendRevoke(ctx context.Context, s *Server, a args) (any, error) {
	if err := a.count(3, 4, "omni_sendrevoke \"fromaddress\" propertyid \"amount\" ( \"memo\" )"); err != nil {
		return nil, err
	}
	return s.sendWith(ctx, func(q *query, req *wallet.Request) error {
		var err error
		if req.From, err = q.address(a, 0); err != nil {
			return err
		}
		id, amount, err := q.propertyAndAmount(a, 1)
		if err != nil {
			return err
		}
		memo, err := a.optText(3)
		if err != nil {
			return err
		}
		if err := q.requireManagedByIssuer(req.From, id); err != nil {
			return err
		}
		if err := q.requireBalance(req.From, id, amount); err != nil {
			return err
		}
		req.Payload = omni.PayloadRevoke(id, uint64(amount), memo)
		return nil
	})
}

func sendCloseCrowdsale(ctx context.Context, s *Server, a args) (any, error) {
	if err := a.count(2, 2, "omni_sendclosecrowdsale \"fromaddress\" propertyid"); err != nil {
		return nil, err
	}
	return s.sendWith(ctx, func(q *query, req *wallet.Request) error {
		var err error
		if req.From, err = q.address(a, 0); err != nil {
			return err
		}
		id, err := a.propertyID(1)
		if err != nil {
			return err
		}
		if _, err := q.requireExisting(id); err != nil {
			return err
		}
		if err := q.requireCrowdsale(id); err != nil {
			return err
		}
		if err := q.requireActiveCrowdsale(id); err != nil {
			return err
		}
		if err := q.requireTokenIssuer(req.From, id); err != nil {
			return err
		}
		req.Payload = omni.PayloadCloseCrowdsale(id)
		return nil
	})
}

func sendChangeIssuer(ctx context.Context, s *Server, a args) (any, error) {
	if err := a.count(3, 3, "omni_sendchangeissuer \"fromaddress\" \"toaddress\" propertyid"); err != nil {
		return nil, err
	}
	return s.sendWith(ctx, func(q *query, req *wallet.Request) error {
		var err error
		if req.From, err = q.address(a, 0); err != nil {
			return err
		}
		if req.To, err = q.address(a, 1); err != nil {
			return err
		}
		id, err := a.propertyID(2)
		if err != nil {
			return err
		}
		if _, err := q.requireExisting(id); err != nil {
			return err
		}
		if err := q.requireTokenIssuer(req.From, id); err != nil {
			return err
		}
		req.Payload = omni.PayloadChangeIssuer(id)
		return nil
	})
}

func sendFreezing(enable bool) nodeHandler {
	name := map[bool]string{true: "omni_sendenablefreezing", false: "omni_senddisablefreezing"}[enable]
	return func(ctx context.Context, s *Server, a args) (any, error) {
		if err := a.count(2, 2, name+" \"fromaddress\" propertyid"); err != nil {
			return nil, err
		}
		return s.sendWith(ctx, func(q *query, req *wallet.Request) error {
			var err error
			if req.From, err = q.address(a, 0); err != nil {
				return err
			}
			id, err := a.propertyID(1)
			if err != nil {
				return err
			}
			if err := q.requireManagedByIssuer(req.From, id); err != nil {
				return err
			}
			if enable {
				req.Payload = omni.PayloadEnableFreezing(id)
			} else {
				req.Payload = omni.PayloadDisableFreezing(id)
			}
			return nil
		})
	}
}

var (
	sendEnableFreezing  = sendFreezing(true)
	sendDisableFreezing = sendFreezing(false)
	sendFreeze          = sendFreezeTokens(true)
	sendUnfreeze        = sendFreezeTokens(false)
)

func sendFreezeTokens(freeze bool) nodeHandler {
	name := map[bool]string{true: "omni_sendfreeze", false: "omni_sendunfreeze"}[freeze]
	return func(ctx context.Context, s *Server, a args) (any, error) {
		if err := a.count(4, 4, name+" \"fromaddress\" \"toaddress\" propertyid amount"); err != nil {
			return nil, err
		}
		return s.sendWith(ctx, func(q *query, req *wallet.Request) error {
			var err error
			if req.From, err = q.address(a, 0); err != nil {
				return err
			}
			target, err := q.address(a, 1)
			if err != nil {
				return err
			}
			id, amount, err := q.propertyAndAmount(a, 2)
			if err != nil {
				return err
			}
			if err := q.requireManagedByIssuer(req.From, id); err != nil {
				return err
			}
			d, err := omni.DecodeAddress(target, q.params)
			if err != nil {
				return err
			}
			// The target is in the payload, there is no reference output.
			req.Payload = omni.PayloadFreeze(freeze, id, uint64(amount), d, q.params)
			return nil
		})
	}
}

func setNonFungibleData(ctx context.Context, s *Server, a args) (any, error) {
	if err := a.count(5, 5, "omni_setnonfungibledata propertyid tokenstart tokenend issuer \"data\""); err != nil {
		return nil, err
	}
	return s.sendWith(ctx, func(q *query, req *wallet.Request) error {
		id, start, end, issuer, data, err := a.nftDataArgs()
		if err != nil {
			return err
		}
		p, err := q.requireExisting(id)
		if err != nil {
			return err
		}
		if err := q.requireNonFungibleProperty(id); err != nil {
			return err
		}
		if err := requireSaneNonFungibleRange(start, end); err != nil {
			return err
		}
		if !issuer {
			ok, err := q.st.NFTContiguous(id, start, end)
			if err != nil {
				return err
			}
			if !ok {
				return errorf(codeInvalidParam, "Range set owned by multiple addresses, set data one owner a time")
			}
		}
		// The sender is the owner of the range, or the issuer for issuer data.
		if req.From, err = q.st.NFTOwner(id, start); err != nil {
			return err
		}
		if issuer {
			req.From = p.Issuer
		}
		req.Payload = omni.PayloadSetNonFungibleData(id, uint64(start), uint64(end), issuer, data)
		return nil
	})
}
