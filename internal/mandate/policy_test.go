package mandate

import (
	"math"
	"strings"
	"testing"
	"time"
)

func base() Mandate {
	return Mandate{ID: "m1", Name: "Home office", Currency: "USD", TotalBudget: 300, MaxPerItem: 150, AutoApproveUnder: 50, AllowedCategories: []string{"computers", "office"}, BlockedMerchants: []string{"ShadyShop"}, MinRating: 4.0}
}

func item(id string, price float64, cat, merchant string, rating float64) CartItem {
	return CartItem{ProductID: id, Title: id, UnitPrice: price, Currency: "USD", Quantity: 1, Category: cat, Merchant: merchant, Rating: rating}
}

func codes(d Decision) string {
	var out []string
	for _, v := range d.Violations {
		out = append(out, v.Code)
	}
	return strings.Join(out, ",")
}

func TestEvaluate(t *testing.T) {
	tests := []struct {
		name         string
		mandate      Mandate
		cart         Cart
		wantAllowed  bool
		wantApproval bool
		wantCodes    string
	}{
		{"simple pass needs approval", base(), Cart{Currency: "USD", Items: []CartItem{item("kb", 109, "computers", "KeyboardCo", 4.7)}}, true, true, ""},
		{"cheap cart auto-approves", base(), Cart{Currency: "USD", Items: []CartItem{item("mat", 39, "office", "OfficeWorks", 4.5)}}, true, false, ""},
		{"exactly at threshold still needs approval", base(), Cart{Currency: "USD", Items: []CartItem{item("x", 50, "office", "OfficeWorks", 4.5)}}, true, true, ""},
		{"per-item cap", base(), Cart{Currency: "USD", Items: []CartItem{item("chair", 349, "office", "OfficeWorks", 4.6)}}, false, true, "item_over_cap,over_budget"},
		{"category blocked", base(), Cart{Currency: "USD", Items: []CartItem{item("kettle", 59.99, "kitchen", "HomeGoods", 4.6)}}, false, true, "category_not_allowed"},
		{"merchant blocked (case-insensitive)", base(), Cart{Currency: "USD", Items: []CartItem{item("hub", 49.99, "computers", "shadyshop", 4.6)}}, false, false, "merchant_blocked"},
		{"rating too low", base(), Cart{Currency: "USD", Items: []CartItem{item("hub", 49.99, "computers", "TechDepot", 3.2)}}, false, false, "rating_too_low"},
		{"unknown rating is ignored", base(), Cart{Currency: "USD", Items: []CartItem{item("hub", 49.99, "computers", "TechDepot", 0)}}, true, false, ""},
		{"over budget across items", base(), Cart{Currency: "USD", Items: []CartItem{item("a", 149, "computers", "TechDepot", 4.5), item("b", 149, "computers", "TechDepot", 4.5), item("c", 10, "office", "TechDepot", 4.5)}}, false, true, "over_budget"},
		{"quantity multiplies", base(), Cart{Currency: "USD", Items: []CartItem{{ProductID: "a", Title: "a", UnitPrice: 100, Currency: "USD", Quantity: 4, Category: "computers", Merchant: "TechDepot", Rating: 4.5}}}, false, true, "over_budget"},
		{"currency mismatch", base(), Cart{Currency: "GBP", Items: []CartItem{item("a", 10, "office", "OfficeWorks", 4.5)}}, false, false, "currency_mismatch"},
		{"empty cart", base(), Cart{Currency: "USD"}, false, true, "empty_cart"},
		{"require_approval overrides threshold", func() Mandate { m := base(); m.RequireApproval = true; return m }(), Cart{Currency: "USD", Items: []CartItem{item("mat", 39, "office", "OfficeWorks", 4.5)}}, true, true, ""},
		{"no categories means any category", func() Mandate { m := base(); m.AllowedCategories = nil; return m }(), Cart{Currency: "USD", Items: []CartItem{item("kettle", 59.99, "kitchen", "HomeGoods", 4.6)}}, true, true, ""},
		{"spent and reserved reduce the budget", func() Mandate { m := base(); m.Spent = 200; m.Reserved = 60; return m }(), Cart{Currency: "USD", Items: []CartItem{item("hub", 49.99, "computers", "TechDepot", 4.6)}}, false, false, "over_budget"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			d := tc.mandate.Evaluate(tc.cart)
			if d.Allowed != tc.wantAllowed {
				t.Fatalf("allowed=%v want %v (violations %s)", d.Allowed, tc.wantAllowed, codes(d))
			}
			if got := codes(d); got != tc.wantCodes {
				t.Fatalf("violations=%q want %q", got, tc.wantCodes)
			}
			if d.Allowed && d.RequiresApproval != tc.wantApproval {
				t.Fatalf("requires_approval=%v want %v", d.RequiresApproval, tc.wantApproval)
			}
		})
	}
}

func TestRemainingAndRounding(t *testing.T) {
	m := base()
	m.Spent = 100.005
	m.Reserved = 0.1 + 0.2
	if got := m.Remaining(); got != 199.69 && got != 199.7 {
		t.Fatalf("remaining = %v", got)
	}
	c := Cart{Currency: "USD", Items: []CartItem{{UnitPrice: 0.1, Quantity: 3}}}
	if c.Total() != 0.3 {
		t.Fatalf("total = %v", c.Total())
	}
}

func TestValidate(t *testing.T) {
	m := base()
	if err := m.Validate(); err != nil {
		t.Fatal(err)
	}
	m.TotalBudget = 0
	if err := m.Validate(); err == nil {
		t.Fatal("expected error for zero budget")
	}
	m = base()
	m.Currency = "dollars"
	if err := m.Validate(); err == nil {
		t.Fatal("expected error for bad currency")
	}
}

func TestValidateBoundsAndRound(t *testing.T) {
	base := Mandate{Name: "x", Currency: "USD", TotalBudget: 100}
	for name, m := range map[string]Mandate{
		"inf budget":   {Name: "x", Currency: "USD", TotalBudget: math.Inf(1)},
		"nan cap":      {Name: "x", Currency: "USD", TotalBudget: 100, MaxPerItem: math.NaN()},
		"huge":         {Name: "x", Currency: "USD", TotalBudget: 1e307},
		"long name":    {Name: strings.Repeat("n", 101), Currency: "USD", TotalBudget: 100},
		"long entries": {Name: "x", Currency: "USD", TotalBudget: 100, AllowedCategories: []string{strings.Repeat("c", 65)}},
	} {
		if err := m.Validate(); err == nil {
			t.Fatalf("%s: expected an error", name)
		}
	}
	if err := base.Validate(); err != nil {
		t.Fatal(err)
	}
	if v := Round(math.Inf(1)); !math.IsInf(v, 1) {
		t.Fatalf("Round(+Inf) = %v", v)
	}
	if Round(1.005) != 1.01 && Round(1.005) != 1 {
		t.Fatalf("Round(1.005) = %v", Round(1.005))
	}
}

func TestMerchantBlockedMatchesDomains(t *testing.T) {
	blocked := []string{"temu.com", "https://www.Shein.com/"}
	for merchant, want := range map[string]bool{"temu.com": true, "www.temu.com": true, "shop.temu.com": true, "temu.co.uk": false, "nottemu.com": false, "shein.com": true, "SHEIN.COM": true, "amazon.com": false, "": false} {
		if got := MerchantBlocked(merchant, blocked); got != want {
			t.Fatalf("MerchantBlocked(%q) = %v, want %v", merchant, got, want)
		}
	}
}

func TestRedactedHidesTheToken(t *testing.T) {
	now := time.Now()
	m := Mandate{Name: "x", Currency: "USD", TotalBudget: 100, VaultID: "VLT1", VaultPayer: "buyer@example.com", VaultedAt: &now}
	r := m.Redacted()
	if r.VaultID != "" || !r.HasSaved || r.VaultPayer != "buyer@example.com" || m.VaultID != "VLT1" {
		t.Fatalf("redacted = %+v", r)
	}
}
