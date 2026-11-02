package api

import (
	"context"
	"fmt"
	"strings"

	"github.com/tbuuol/omnilite-standalone/internal/chain"
	"github.com/tbuuol/omnilite-standalone/internal/omni"
	"github.com/tbuuol/omnilite-standalone/internal/state"
	"github.com/tbuuol/omnilite-standalone/internal/wallet"
)

// DEx trading (rpctx.cpp). Buying takes two steps, both started by the
// user: omni_senddexaccept reserves the tokens, omni_senddexpay pays for
// them. Nothing here pays on its own.

// DEx actions (CMPTransaction::NEW, UPDATE, CANCEL).
const (
	dexNew    = 1
	dexUpdate = 2
	dexCancel = 3
)

func registerDEx() {
	for name, h := range map[string]nodeHandler{
		"omni_senddexsell":        sendDExSell,
		"omni_sendnewdexorder":    sendDExOrder(dexNew),
		"omni_sendupdatedexorder": sendDExOrder(dexUpdate),
		"omni_sendcanceldexorder": sendDExOrder(dexCancel),
		"omni_senddexaccept":      sendDExAccept,
		"omni_senddexpay":         sendDExPay,
		"omni_listdexaccepts":     listDExAccepts,
	} {
		nodeMethods[name] = h
		walletMethods[name] = true
	}
	methods["omni_createpayload_dexaccept"] = createPayloadDExAccept
}

// dexWindow mirrors ParseDExPaymentWindow.
func (a args) dexWindow(i int) (uint8, error) {
	n, err := a.ranged(i, 1, 255, codeType, "Payment window must be within 1-255 blocks")
	return uint8(n), err
}

// dexFee mirrors ParseDExFee.
func (a args) dexFee(i int) (int64, error) {
	s, err := a.str(i)
	if err != nil {
		return 0, err
	}
	if n := strToInt64(s, true); n >= 0 {
		return n, nil
	}
	return 0, errorf(codeType, "Mininmum accept fee must be positive")
}

// requirePrimaryToken applies RequirePrimaryToken while FREEDEX is not
// active.
func (q *query) requirePrimaryToken(id uint32) error {
	h, err := q.height()
	if err != nil {
		return err
	}
	free, err := q.st.FeatureActive(chain.FeatureFreeDEx, h)
	if err != nil || free {
		return err
	}
	if id < 1 || id > 2 {
		return errorf(codeInvalidParam, "Property identifier must be 1 (OMN) or 2 (TOMN)")
	}
	return nil
}

// requireMatchingOffer mirrors RequireMatchingDExOffer and returns the offer.
func (q *query) requireMatchingOffer(seller string, id uint32) (*state.Offer, error) {
	o, err := q.st.Offer(seller, id)
	if err != nil {
		return nil, err
	}
	if o == nil {
		return nil, errorf(codeType, "No matching sell offer on the distributed exchange")
	}
	return o, nil
}

// dexOrder are the parameters of a sell offer.
type dexOrder struct {
	id              uint32
	amount, desired int64
	window          uint8
	minFee          int64
}

// parseDExOrder reads property, amount, desired LTC, window and fee
// starting at i; only the property for a cancel.
func (q *query) parseDExOrder(a args, i int, action int64) (*dexOrder, error) {
	o := &dexOrder{}
	var err error
	if o.id, err = a.propertyID(i); err != nil {
		return nil, err
	}
	if action == dexCancel {
		return o, nil
	}
	if _, o.amount, err = q.propertyAndAmount(a, i); err != nil {
		return nil, err
	}
	if o.desired, err = a.amount(i+2, true); err != nil {
		return nil, err
	}
	if o.window, err = a.dexWindow(i + 3); err != nil {
		return nil, err
	}
	o.minFee, err = a.dexFee(i + 4)
	return o, err
}

// prepareDExSell checks an offer like omni_senddexsell and fills req.
func (q *query) prepareDExSell(req *wallet.Request, o *dexOrder, action int64) error {
	if err := q.requirePrimaryToken(o.id); err != nil {
		return err
	}
	switch action {
	case dexNew:
		if err := q.requireBalance(req.From, o.id, o.amount); err != nil {
			return err
		}
		has, err := q.st.HasOffer(req.From)
		if err != nil {
			return err
		}
		if has {
			return errorf(codeType, "Another active sell offer from the given address already exists on the distributed exchange")
		}
	case dexUpdate:
		if err := q.requireBalance(req.From, o.id, o.amount); err != nil {
			return err
		}
		if _, err := q.requireMatchingOffer(req.From, o.id); err != nil {
			return err
		}
	case dexCancel:
		if _, err := q.requireMatchingOffer(req.From, o.id); err != nil {
			return err
		}
	}
	req.Payload = omni.PayloadDExSell(o.id, uint64(o.amount), uint64(o.desired), o.window, uint64(o.minFee), uint8(action))
	if action != dexCancel {
		// The offered tokens are pending until the offer confirms.
		req.Property, req.Amount = o.id, o.amount
	}
	return nil
}

func sendDExSell(ctx context.Context, s *Server, a args) (any, error) {
	if err := a.count(7, 7, "omni_senddexsell \"fromaddress\" propertyidforsale \"amountforsale\" \"amountdesired\" paymentwindow minacceptfee action"); err != nil {
		return nil, err
	}
	return s.sendWith(ctx, func(q *query, req *wallet.Request) error {
		var err error
		if req.From, err = q.address(a, 0); err != nil {
			return err
		}
		if _, err := a.propertyID(1); err != nil {
			return err
		}
		action, err := a.ranged(6, 1, 3, codeType, "Invalid action (1 = new, 2 = update, 3 = cancel only)")
		if err != nil {
			return err
		}
		o, err := q.parseDExOrder(a, 1, action)
		if err != nil {
			return err
		}
		return q.prepareDExSell(req, o, action)
	})
}

func sendDExOrder(action int64) nodeHandler {
	name := map[int64]string{dexNew: "omni_sendnewdexorder", dexUpdate: "omni_sendupdatedexorder", dexCancel: "omni_sendcanceldexorder"}[action]
	n, usage := 6, " \"fromaddress\" propertyidforsale \"amountforsale\" \"amountdesired\" paymentwindow minacceptfee"
	if action == dexCancel {
		n, usage = 2, " \"fromaddress\" propertyidforsale"
	}
	return func(ctx context.Context, s *Server, a args) (any, error) {
		if err := a.count(n, n, name+usage); err != nil {
			return nil, err
		}
		return s.sendWith(ctx, func(q *query, req *wallet.Request) error {
			var err error
			if req.From, err = q.address(a, 0); err != nil {
				return err
			}
			o, err := q.parseDExOrder(a, 1, action)
			if err != nil {
				return err
			}
			return q.prepareDExSell(req, o, action)
		})
	}
}

func sendDExAccept(ctx context.Context, s *Server, a args) (any, error) {
	if err := a.count(4, 5, "omni_senddexaccept \"fromaddress\" \"toaddress\" propertyid \"amount\" ( override )"); err != nil {
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
		id, amount, err := q.propertyAndAmount(a, 2)
		if err != nil {
			return err
		}
		override := false
		if len(a) > 4 {
			if override, err = a.boolean(4); err != nil {
				return err
			}
		}
		if err := q.requirePrimaryToken(id); err != nil {
			return err
		}
		offer, err := q.requireMatchingOffer(req.To, id)
		if err != nil {
			return err
		}
		if !override {
			// RequireSaneDExFee (the message says 0.02, the limit is 0.01)
			// and RequireSaneDExPaymentWindow.
			if offer.MinFee > 1_000_000 {
				return errorf(codeType, "Minimum accept fee is higher than 0.02 LTC (use override = true to continue)")
			}
			if offer.BlockTimeLimit < 10 {
				return errorf(codeType, "Payment window is less than 10 blocks (use override = true to continue)")
			}
		}
		// The accept must pay at least the offer's minimum fee.
		req.MinFee = offer.MinFee
		req.Payload = omni.PayloadDExAccept(id, uint64(amount))
		return nil
	})
}

// formatMoney mirrors FormatMoney: at least two decimals, trailing zeros cut.
func formatMoney(n int64) string {
	s := formatDivisible(n)
	if n < 0 {
		s = "-" + s
	}
	for strings.HasSuffix(s, "0") && len(s)-strings.IndexByte(s, '.') > 3 {
		s = s[:len(s)-1]
	}
	return s
}

// MinPayBlocks is the least number of blocks left in the payment window
// for omni_senddexpay without override: with one block left, the payment
// must make the very next block or the LTC buy nothing.
const MinPayBlocks = 2

func sendDExPay(ctx context.Context, s *Server, a args) (any, error) {
	// OmniLite takes four parameters; override is an addition for the
	// payment window check below.
	if err := a.count(4, 5, "omni_senddexpay \"fromaddress\" \"toaddress\" propertyid \"amount\" ( override )"); err != nil {
		return nil, err
	}
	return s.sendWith(ctx, func(q *query, req *wallet.Request) error {
		buyerText, err := a.text(0)
		if err != nil {
			return err
		}
		sellerText, err := a.text(1)
		if err != nil {
			return err
		}
		id, err := a.propertyID(2)
		if err != nil {
			return err
		}
		amount, err := a.amount(3, true)
		if err != nil {
			return err
		}
		override := false
		if len(a) > 4 {
			if override, err = a.boolean(4); err != nil {
				return err
			}
		}
		buyer, err := omni.DecodeAddress(buyerText, q.params)
		if err != nil {
			return errorf(codeInvalidAddress, "Invalid buyer address")
		}
		seller, err := omni.DecodeAddress(sellerText, q.params)
		if err != nil {
			return errorf(codeInvalidAddress, "Invalid seller address")
		}
		req.From = buyer.Encode(q.params)
		sellerAddr := seller.Encode(q.params)
		if err := q.requirePrimaryToken(id); err != nil {
			return err
		}
		acc, err := q.st.Accept(sellerAddr, req.From, id)
		if err != nil {
			return err
		}
		if acc == nil {
			return errorf(codeType, "No matching accept order on the distributed exchange")
		}
		toPay := state.DesiredLTC(acc.OfferAmountOriginal, acc.LTCDesiredOriginal, acc.AmountRemaining)
		tokens, err := q.formatMP(id, acc.AmountRemaining)
		if err != nil {
			return err
		}
		if amount > toPay {
			return errorf(codeMisc, fmt.Sprintf("Paying more than required: %s LTC to pay for %s tokens", formatMoney(toPay), tokens))
		}
		div, err := q.st.Divisible(id)
		if err != nil {
			return err
		}
		if !div && amount < toPay {
			return errorf(codeMisc, fmt.Sprintf("Paying less than required: %s LTC to pay for %s tokens", formatMoney(toPay), tokens))
		}
		h, err := q.height()
		if err != nil {
			return err
		}
		left := acc.Block + int64(acc.BlockTimeLimit) - h
		if left < 1 {
			return errorf(codeMisc, "The payment window of this accept has ended")
		}
		if left < MinPayBlocks && !override {
			return errorf(codeMisc, "Only 1 block is left in the payment window; if the payment confirms later, the LTC buy nothing (use override = true to continue)")
		}
		req.Pay = &wallet.Payment{Seller: sellerAddr, Property: id, Amount: amount}
		return nil
	})
}

func createPayloadDExAccept(q *query, a args) (any, error) {
	if err := a.count(2, 2, "omni_createpayload_dexaccept propertyid \"amount\""); err != nil {
		return nil, err
	}
	id, amount, err := q.propertyAndAmount(a, 0)
	if err != nil {
		return nil, err
	}
	return payloadHex(omni.PayloadDExAccept(id, uint64(amount)))
}

// listDExAccepts is not in OmniLite: it lists the open accepts where an
// address of the node wallet buys or sells, with the blocks left to pay.
func listDExAccepts(ctx context.Context, s *Server, a args) (any, error) {
	if err := a.count(0, 0, "omni_listdexaccepts"); err != nil {
		return nil, err
	}
	list, err := s.Wallet.OpenAccepts(ctx)
	if err != nil {
		return nil, err
	}
	res := array{}
	err = s.read(ctx, func(q *query) error {
		for _, acc := range list {
			amount, err := q.formatMP(acc.Property, acc.Amount)
			if err != nil {
				return err
			}
			role := "buyer"
			if !acc.MineBuyer {
				role = "seller"
			}
			res = append(res, (&object{}).
				add("seller", acc.Seller).
				add("buyer", acc.Buyer).
				add("propertyid", acc.Property).
				add("amount", amount).
				add("amounttopay", formatDivisible(acc.AmountToPay)).
				add("block", acc.Block).
				add("blocksleft", acc.BlocksLeft).
				add("role", role))
		}
		return nil
	})
	return res, err
}
