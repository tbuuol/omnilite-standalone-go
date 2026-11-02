package state

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"math"
	"testing"

	"github.com/tbuuol/omnilite-standalone/internal/omni"
	"github.com/tbuuol/omnilite-standalone/internal/store"
)

// Vectors from OmniLite's crowdsale_participation_tests.cpp.
func TestCalculateFundraiserVectors(t *testing.T) {
	tests := []struct {
		inflate                bool
		amount                 int64
		bonus                  uint8
		deadline, now, perUnit int64
		issuerPerc             uint8
		total                  int64
		user, issuer           int64
		closed                 bool
	}{
		{true, 3000000000, 6, 1407064860000, 1407877014, 3133700000000, 10, 0, 8384883669867978007, 838488366986797800, true},
		{true, math.MaxInt64, 255, math.MaxInt64, 0, math.MaxInt64, 255, math.MaxInt64 - 1, 1, 0, true},
		{false, 1000000000, 255, 500000000, 500007119, 50, 19, 0, 500, 95, false},
	}
	for i, tt := range tests {
		user, issuer, closed := calculateFundraiser(tt.inflate, tt.amount, tt.bonus, tt.deadline, tt.now, tt.perUnit, tt.issuerPerc, tt.total)
		if user != tt.user || issuer != tt.issuer || closed != tt.closed {
			t.Errorf("vector %d: got %d %d %v, want %d %d %v", i, user, issuer, closed, tt.user, tt.issuer, tt.closed)
		}
	}
}

// Vectors from OmniLite's dex_purchase_tests.cpp.
func TestDExPurchaseVectors(t *testing.T) {
	const max = math.MaxInt64
	tests := [][4]int64{
		{0, 1, 1, 0}, {1, 1, 1, 1}, {2, 4, 8, 4}, {3, 9, 9, 3}, {7, max, max, 7}, {max, max, 1, 1}, {max, max, max, max},
		{2345678901234567890, max, max, 2345678901234567890}, {9111111111111111111, max, max, 9111111111111111111},
		{10, 17, 13, 7}, {14, 19, 11, 8},
		{100000000000000000, 200000000000000000, 1000000000000000000, 499999999999999999},
		{100000000000000001, 200000000000000001, 1000000000000000000, 500000000000000000},
		{0, 100000000, 10000, 0},
	}
	for _, tt := range tests {
		if got := dexPurchase(tt[1], tt[2], tt[3]); got != tt[0] {
			t.Errorf("dexPurchase(%d, %d, %d) = %d, want %d", tt[1], tt[2], tt[3], got, tt[0])
		}
	}
}

// Port of OmniLite's nftdb_tests.cpp.
func TestNFTSequence(t *testing.T) {
	h := newHarness(t)
	h.read(func(s *State) {
		must := func(err error) {
			t.Helper()
			if err != nil {
				t.Fatal(err)
			}
		}
		owner := func(token int64) string {
			o, err := s.nftOwner(50, token)
			must(err)
			return o
		}
		contiguous := func(a, b int64) bool {
			ok, err := s.nftContiguous(50, a, b)
			must(err)
			return ok
		}
		create := func(amount int64, to string, start, end int64) {
			t.Helper()
			a, b, err := s.nftCreate(50, amount, to, "")
			must(err)
			if a != start || b != end {
				t.Fatalf("created %d-%d, want %d-%d", a, b, start, end)
			}
		}
		create(1000, "Alice", 1, 1000)
		if owner(1) != "Alice" || owner(1000) != "Alice" || owner(454) != "Alice" {
			t.Fatal("test A")
		}
		create(1000, "Alice", 1001, 2000)
		if !contiguous(1, 2000) {
			t.Fatal("test B: ranges not merged")
		}
		create(1000, "Bob", 2001, 3000)
		if owner(2001) != "Bob" || contiguous(2000, 2001) {
			t.Fatal("test C")
		}
		must(s.nftMove(50, 2001, 3000, "Bob", "Charles"))
		if owner(2001) != "Charles" || owner(3000) != "Charles" {
			t.Fatal("test D")
		}
		create(1000, "David", 3001, 4000)
		if contiguous(3000, 3001) {
			t.Fatal("test E: merged too early")
		}
		must(s.nftMove(50, 3001, 4000, "David", "Charles"))
		if owner(4000) != "Charles" || !contiguous(3000, 3001) {
			t.Fatal("test E")
		}
		must(s.nftMove(50, 500, 500, "Alice", "Bob"))
		if owner(500) != "Bob" || owner(499) != "Alice" || owner(501) != "Alice" {
			t.Fatal("test F")
		}
		must(s.nftMove(50, 2300, 2400, "Charles", "Bob"))
		if owner(2300) != "Bob" || owner(2400) != "Bob" || !contiguous(2300, 2400) || owner(2401) != "Charles" {
			t.Fatal("test G")
		}
		if err := s.nftMove(50, 2300, 2500, "Bob", "Alice"); err == nil {
			t.Fatal("moved a range Bob does not fully own")
		}
	})
}

func TestNFTTransactions(t *testing.T) {
	h := newHarness(t)
	h.height = 2262000 // MSC_NONFUNGIBLE_BLOCK on mainnet
	nft := managed("Art")
	nft.PropType = PropTypeNonFungible
	g := grant(3, 10, bob)
	g.NFTData = "grant data"
	h.block(txIn{alice, nft}, txIn{alice, g})
	h.read(func(s *State) {
		if r, _, _ := s.nftFind(3, 5, nftGrantData); r.Value != "grant data" || r.End != 10 {
			t.Fatalf("grant data %+v", r)
		}
	})

	send := func(start, end uint64, to string) omni.Message {
		return omni.Message{Type: omni.TypeSendNonFungible, Property: 3, TokenStart: start, TokenEnd: end, Receiver: to}
	}
	data := func(start, end uint64, issuer uint8, d string) omni.Message {
		return omni.Message{Type: omni.TypeNonFungibleData, Property: 3, TokenStart: start, TokenEnd: end, NFTDataType: issuer, NFTData: d}
	}
	wantCodes(t, h.block(
		txIn{bob, send(3, 4, carol)},
		txIn{bob, send(3, 4, carol)},
		txIn{bob, send(0, 4, carol)},
		txIn{bob, send(5, 4, carol)},
		txIn{bob, send(1, 1, bob)},
		txIn{carol, data(3, 4, 0, "mine")},
		txIn{bob, data(3, 4, 0, "not mine")},
		txIn{carol, data(1, 10, 1, "issuer data before feature 18")},
		txIn{alice, send(1, 1, bob)},
		txIn{alice, omni.Message{Type: omni.TypeSimpleSend, Property: 3, Amount: 1, Receiver: carol}},
	), 0, -85033, -85025, -85027, 0, 0, -85030, 0, -85032, -85021)
	if h.balance(bob, 3) != 8 || h.balance(carol, 3) != 2 {
		t.Fatalf("balances bob %d carol %d", h.balance(bob, 3), h.balance(carol, 3))
	}
	h.read(func(s *State) {
		if o, _ := s.nftOwner(3, 3); o != carol {
			t.Fatalf("token 3 owner %q", o)
		}
		if r, _, _ := s.nftFind(3, 4, nftHolderData); r.Value != "mine" {
			t.Fatalf("holder data %+v", r)
		}
	})
}

func TestFreeze(t *testing.T) {
	h := newHarness(t)
	h.block(txIn{alice, managed("Managed")}, txIn{alice, grant(3, 100, bob)})
	freeze := func(on bool, who string) omni.Message {
		typ := uint16(omni.TypeFreezeTokens)
		if !on {
			typ = omni.TypeUnfreezeTokens
		}
		return omni.Message{Type: typ, Property: 3, Receiver: who}
	}
	enable := omni.Message{Type: omni.TypeEnableFreezing, Property: 3}
	disable := omni.Message{Type: omni.TypeDisableFreezing, Property: 3}

	wantCodes(t, h.block(
		txIn{alice, freeze(true, bob)},
		txIn{bob, enable},
		txIn{alice, enable},
		txIn{alice, enable},
		txIn{alice, freeze(true, bob)},
		txIn{alice, freeze(true, bob)},
		txIn{bob, send(3, 1, carol)},
		txIn{alice, freeze(false, bob)},
		txIn{alice, freeze(false, bob)},
		txIn{bob, send(3, 1, carol)},
		txIn{alice, freeze(true, bob)},
		txIn{alice, disable},
		txIn{bob, send(3, 1, carol)},
		txIn{alice, disable},
	), -82047, -82043, 0, -82049, 0, -82050, -9003, 0, -82048, 0, 0, 0, 0, -82047)
	if h.balance(carol, 3) != 2 {
		t.Fatalf("carol = %d", h.balance(carol, 3))
	}
}

func TestCrowdsale(t *testing.T) {
	h := newHarness(t)
	h.block(txIn{bob, fixed(1, 100_00000000, "Coin")}) // property 3, bought with
	sale := omni.Message{
		Type: omni.TypeCreatePropertyVar, Ecosystem: 1, PropType: PropTypeDivisible, Name: "Sale",
		Property: 3, Amount: 10_00000000, Deadline: uint64(h.time() + 14*24*3600), EarlyBird: 10, Percentage: 20,
	}
	past := sale
	past.Deadline = uint64(h.time() - 1)
	cross := sale
	cross.Property = TestEcoFirst
	wantCodes(t, h.block(
		txIn{alice, past},
		txIn{alice, cross},
		txIn{alice, sale},
		txIn{alice, sale},
		txIn{alice, omni.Message{Type: omni.TypeChangeIssuer, Property: 3, Receiver: alice}},
	), -40038, -40050, 0, -40039, -82043)

	buyTime := h.time()
	h.block(txIn{bob, send(3, 1_00000000, alice)})
	var user, issuer int64
	h.read(func(s *State) {
		c, _ := s.Crowdsale(alice)
		if c == nil || c.PropertyID != 4 || c.UserCreated == 0 || len(c.Purchases) != 1 {
			t.Fatalf("crowdsale %+v", c)
		}
		user, issuer = c.UserCreated, c.IssuerCreated
	})
	if h.balance(bob, 4) != user || h.balance(alice, 4) != issuer || h.balance(alice, 3) != 1_00000000 {
		t.Fatalf("bob %d alice %d (want %d %d)", h.balance(bob, 4), h.balance(alice, 4), user, issuer)
	}
	wantUser, wantIssuer, _ := calculateFundraiser(false, 1_00000000, sale.EarlyBird, int64(sale.Deadline), buyTime, int64(sale.Amount), sale.Percentage, 0)
	if user != wantUser || issuer != wantIssuer || user == 0 || issuer == 0 {
		t.Fatalf("user %d issuer %d, want %d %d", user, issuer, wantUser, wantIssuer)
	}

	wantCodes(t, h.block(
		txIn{bob, omni.Message{Type: omni.TypeCloseCrowdsale, Property: 4}},
		txIn{alice, omni.Message{Type: omni.TypeCloseCrowdsale, Property: 3}},
		txIn{alice, omni.Message{Type: omni.TypeCloseCrowdsale, Property: 4}},
	), -40040, -40041, 0)
	h.read(func(s *State) {
		p, _ := s.Property(4)
		if c, _ := s.Crowdsale(alice); c != nil || !p.CloseEarly || len(p.History) != 1 {
			t.Fatalf("crowdsale still open or property %+v", p)
		}
	})

	// A crowdsale past its deadline ends at the start of the next block.
	short := sale
	short.Name = "Short"
	short.Deadline = uint64(h.time() + 300)
	h.block(txIn{carol, short})
	h.block()
	h.read(func(s *State) {
		if c, _ := s.Crowdsale(carol); c != nil {
			t.Fatal("expired crowdsale still active")
		}
	})
}

func TestSendToOwners(t *testing.T) {
	h := newHarness(t)
	h.block(txIn{alice, fixed(1, 1000, "Div")}, txIn{alice, send(3, 300, bob)}, txIn{alice, send(3, 100, carol)})
	sto := omni.Message{Type: omni.TypeSendToOwners, Property: 3, Amount: 41}
	// Nobody owns OMNI on OmniLite, so the fee cannot be paid.
	wantCodes(t, h.block(txIn{alice, sto}), -50027)

	h.mutate(func(s *State) {
		if err := s.mustUpdate(alice, PropertyOmni, 5, Balance); err != nil {
			t.Fatal(err)
		}
	})
	res := h.block(txIn{alice, sto}, txIn{carol, omni.Message{Type: omni.TypeSendToOwners, Property: 3, Amount: 1000}})
	wantCodes(t, res, 0, -50025)
	// bob owns 300 and carol 100 of the 400 others hold: 31 (30.75 up) and 10.
	if len(res[0].STO) != 2 || res[0].STO[0] != (STOReceive{bob, 31}) || res[0].STO[1] != (STOReceive{carol, 10}) {
		t.Fatalf("sto = %+v", res[0].STO)
	}
	if h.balance(alice, PropertyOmni) != 3 || h.balance(alice, 3) != 559 {
		t.Fatalf("alice omni %d tokens %d", h.balance(alice, PropertyOmni), h.balance(alice, 3))
	}
}

func TestDEx(t *testing.T) {
	h := newHarness(t)
	h.block(txIn{alice, fixed(1, 1000, "Div")})
	offer := func(sub uint8, amount uint64) omni.Message {
		return omni.Message{Type: omni.TypeTradeOffer, Version: 1, Property: 3, Amount: amount,
			AmountDesired: 1_00000000, BlockTimeLimit: 10, MinFee: 1000, Subaction: sub}
	}
	accept := omni.Message{Type: omni.TypeAcceptOffer, Property: 3, Amount: 60, Receiver: alice}

	h.fee = 2000
	wantCodes(t, h.block(
		txIn{alice, offer(dexUpdate, 100)},
		txIn{alice, offer(dexNew, 2000)},
		txIn{alice, offer(dexNew, 100)},
		txIn{alice, offer(dexNew, 100)},
	), -70049, -10025, 0, -70048)
	h.fee = 500
	wantCodes(t, h.block(txIn{bob, accept}), -20105)
	h.fee = 2000
	wantCodes(t, h.block(txIn{bob, accept}, txIn{bob, accept}), 0, -20205)

	hash1 := h.hash()
	var res *Result
	h.mutate(func(s *State) {
		var err error
		res, err = s.ApplyPayment(&Payment{Txid: "pay", Block: h.height, Sender: bob,
			Outputs: []omni.DExPayment{{Vout: 1, Address: carol, Value: 1}, {Vout: 2, Address: alice, Value: 50000000}}})
		if err != nil {
			t.Fatal(err)
		}
	})
	if res == nil || len(res.Payments) != 1 || res.Payments[0] != (Purchase{2, alice, 3, 50}) {
		t.Fatalf("payment %+v", res)
	}
	if h.hash() == hash1 {
		t.Fatal("consensus hash did not change")
	}
	h.read(func(s *State) {
		if r, _ := s.Balance(alice, 3, SellOfferReserve); r != 40 {
			t.Fatalf("sell reserve %d", r)
		}
		if r, _ := s.Balance(alice, 3, AcceptReserve); r != 10 {
			t.Fatalf("accept reserve %d", r)
		}
	})
	if h.balance(bob, 3) != 50 {
		t.Fatalf("bob = %d", h.balance(bob, 3))
	}

	// The accept expires after its payment window and the rest goes back
	// to the offer; cancelling the offer returns it to the balance.
	for i := 0; i < 10; i++ {
		h.block()
	}
	h.read(func(s *State) {
		if r, _ := s.Balance(alice, 3, SellOfferReserve); r != 50 {
			t.Fatalf("sell reserve after expiry %d", r)
		}
	})
	wantCodes(t, h.block(txIn{alice, offer(dexCancel, 0)}), 0)
	if h.balance(alice, 3) != 950 {
		t.Fatalf("alice = %d", h.balance(alice, 3))
	}
}

func TestConsensusHashEmpty(t *testing.T) {
	h := newHarness(t)
	// An empty state hashes only the issuers of OMNI and TOMNI.
	got := h.hash()
	want := reverseHex(sha256Hex(fmt.Sprintf("1|%s2|%s", h.params.Exodus, h.params.Exodus)))
	if got != want {
		t.Fatalf("hash %s, want %s", got, want)
	}
}

// mutate changes the state directly in its own block, for set-ups the
// transactions cannot reach.
func (h *harness) mutate(fn func(s *State)) {
	h.t.Helper()
	ctx := context.Background()
	prev := ""
	if tip, _ := h.st.Tip(ctx); tip != nil {
		prev = tip.Hash
	}
	btx, err := h.st.Begin(ctx, store.Block{Height: h.height, Hash: fmt.Sprintf("block%d", h.height), PrevHash: prev})
	if err != nil {
		h.t.Fatal(err)
	}
	fn(New(btx, h.params, h.opt))
	if err := btx.Commit(); err != nil {
		h.t.Fatal(err)
	}
	h.height++
}

func (h *harness) hash() string {
	h.t.Helper()
	v, err := New(h.st.View(context.Background()), h.params, h.opt).ConsensusHash()
	if err != nil {
		h.t.Fatal(err)
	}
	return v
}

func sha256Hex(s string) string {
	sum := sha256.Sum256([]byte(s))
	return hex.EncodeToString(sum[:])
}

func reverseHex(h string) string {
	b, _ := hex.DecodeString(h)
	for i, j := 0, len(b)-1; i < j; i, j = i+1, j-1 {
		b[i], b[j] = b[j], b[i]
	}
	return hex.EncodeToString(b)
}
