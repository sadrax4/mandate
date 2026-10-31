// Package mandate is the policy engine at the heart of the project.
//
// A Mandate is the authority a human gives an AI shopping agent: a budget,
// per-item limits, category and merchant rules and an approval threshold. The
// agent can research and propose freely, but nothing reaches PayPal unless the
// proposed cart passes Evaluate, and nothing is paid without a human approving
// it (either in the Mandate UI or inside PayPal checkout itself).
package mandate

import (
	"fmt"
	"math"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"
)

// Mandate is a spending authority granted to the agent.
type Mandate struct {
	ID                string    `json:"id"`
	Name              string    `json:"name"`
	Currency          string    `json:"currency"`
	TotalBudget       float64   `json:"total_budget"`
	MaxPerItem        float64   `json:"max_per_item"`       // 0 = no per-item cap
	AutoApproveUnder  float64   `json:"auto_approve_under"` // carts cheaper than this skip human review (0 = always review)
	AllowedCategories []string  `json:"allowed_categories"` // empty = any category
	BlockedMerchants  []string  `json:"blocked_merchants"`  // case-insensitive
	MinRating         float64   `json:"min_rating"`         // 0 = ignore ratings
	RequireApproval   bool      `json:"require_approval"`   // true = always review, ignores AutoApproveUnder
	Spent             float64   `json:"spent"`              // captured payments
	Reserved          float64   `json:"reserved"`           // approved, awaiting capture
	CreatedAt         time.Time `json:"created_at"`

	// SavePaymentMethod asks PayPal to save (vault) the buyer's PayPal account
	// at the first successful checkout under this mandate. Later carts are
	// paid with the saved account: approved carts with one click, carts under
	// AutoApproveUnder with no click at all. The mandate stays the only
	// authority; the agent still has no way to pay.
	SavePaymentMethod bool `json:"save_payment_method"`
	// VaultID is the PayPal payment token saved for this mandate, empty until
	// the first vaulted checkout completes. It is only usable with the
	// merchant's own API credentials.
	VaultID    string     `json:"vault_id,omitempty"`
	VaultPayer string     `json:"vault_payer,omitempty"`
	VaultedAt  *time.Time `json:"vaulted_at,omitempty"`
	// HasSaved is what the API reports instead of the token id (see Redacted).
	HasSaved bool `json:"has_saved_payment"`
	// Demo marks the one mandate the dashboard's "Create demo mandate"
	// button creates; a second click returns it instead of making another.
	Demo bool `json:"demo,omitempty"`
}

// HasSavedPayment reports whether a saved PayPal account can pay for carts
// under this mandate without a checkout hop.
func (m Mandate) HasSavedPayment() bool { return m.VaultID != "" }

// SuggestionFields are the mandate fields an agent may ask a human to change.
var SuggestionFields = []string{"total_budget", "max_per_item", "min_rating", "auto_approve_under", "allowed_categories", "blocked_merchants"}

// Amend returns a copy of the mandate with one suggested change applied:
// a new number for the numeric fields, categories added for
// allowed_categories, merchants removed for blocked_merchants. It returns a
// human-readable description of the change, or an error when the change is
// meaningless or the result would not be a valid mandate.
func (m Mandate) Amend(field string, value any) (Mandate, string, error) {
	out := m
	out.AllowedCategories = append([]string(nil), m.AllowedCategories...)
	out.BlockedMerchants = append([]string(nil), m.BlockedMerchants...)
	num := func() (float64, error) {
		switch v := value.(type) {
		case float64:
			return v, nil
		case int:
			return float64(v), nil
		case string:
			return strconv.ParseFloat(strings.TrimSpace(strings.TrimPrefix(v, "$")), 64)
		}
		return 0, fmt.Errorf("%s needs a number", field)
	}
	list := func() ([]string, error) {
		switch v := value.(type) {
		case string:
			return []string{strings.ToLower(strings.TrimSpace(v))}, nil
		case []string:
			return v, nil
		case []any:
			var xs []string
			for _, x := range v {
				if s, ok := x.(string); ok && strings.TrimSpace(s) != "" {
					xs = append(xs, strings.ToLower(strings.TrimSpace(s)))
				}
			}
			return xs, nil
		}
		return nil, fmt.Errorf("%s needs a list of names", field)
	}
	var desc string
	switch field {
	case "total_budget", "max_per_item", "min_rating", "auto_approve_under":
		v, err := num()
		if err != nil {
			return m, "", err
		}
		v = Round(v)
		cur := map[string]*float64{"total_budget": &out.TotalBudget, "max_per_item": &out.MaxPerItem, "min_rating": &out.MinRating, "auto_approve_under": &out.AutoApproveUnder}[field]
		if *cur == v {
			return m, "", fmt.Errorf("%s is already %.2f", field, v)
		}
		if committed := Round(m.Spent + m.Reserved); field == "total_budget" && v < committed {
			return m, "", fmt.Errorf("total_budget cannot go below what is already spent or reserved (%.2f)", committed)
		}
		desc = fmt.Sprintf("%s %.2f → %.2f", field, *cur, v)
		*cur = v
	case "allowed_categories":
		xs, err := list()
		if err != nil || len(xs) == 0 {
			return m, "", fmt.Errorf("allowed_categories needs at least one category")
		}
		if len(out.AllowedCategories) == 0 {
			return m, "", fmt.Errorf("every category is already allowed")
		}
		var added []string
		for _, x := range xs {
			if !contains(out.AllowedCategories, x) {
				out.AllowedCategories = append(out.AllowedCategories, x)
				added = append(added, x)
			}
		}
		if len(added) == 0 {
			return m, "", fmt.Errorf("those categories are already allowed")
		}
		desc = "allow " + strings.Join(added, ", ")
	case "blocked_merchants":
		xs, err := list()
		if err != nil || len(xs) == 0 {
			return m, "", fmt.Errorf("blocked_merchants needs at least one merchant")
		}
		var removed []string
		var keep []string
		for _, b := range out.BlockedMerchants {
			// Remove the entry that blocks any named merchant (unblocking
			// "us.temu.com" removes "temu.com"), with the rule's own matching.
			blocks := false
			for _, x := range xs {
				if MerchantBlocked(x, []string{b}) {
					blocks = true
				}
			}
			if blocks {
				removed = append(removed, b)
			} else {
				keep = append(keep, b)
			}
		}
		if len(removed) == 0 {
			return m, "", fmt.Errorf("those merchants are not blocked")
		}
		out.BlockedMerchants = keep
		desc = "unblock " + strings.Join(removed, ", ")
	default:
		return m, "", fmt.Errorf("unknown field %q", field)
	}
	if err := out.Validate(); err != nil {
		return m, "", err
	}
	return out, desc, nil
}

func contains(xs []string, x string) bool {
	for _, v := range xs {
		if strings.EqualFold(v, x) {
			return true
		}
	}
	return false
}

// Remaining is the budget still available for new proposals.
func (m Mandate) Remaining() float64 {
	return Round(m.TotalBudget - m.Spent - m.Reserved)
}

// CartItem is one line of a proposed purchase.
type CartItem struct {
	ProductID string  `json:"product_id"`
	Title     string  `json:"title"`
	Merchant  string  `json:"merchant"`
	Category  string  `json:"category"`
	UnitPrice float64 `json:"unit_price"`
	Currency  string  `json:"currency"`
	Quantity  int     `json:"quantity"`
	Rating    float64 `json:"rating,omitempty"`
	Reason    string  `json:"reason,omitempty"`
}

// LineTotal is price × quantity.
func (c CartItem) LineTotal() float64 { return Round(c.UnitPrice * float64(c.Quantity)) }

// Cart is a set of items the agent wants to buy.
type Cart struct {
	Items    []CartItem `json:"items"`
	Currency string     `json:"currency"`
}

// Total sums all line totals.
func (c Cart) Total() float64 {
	t := 0.0
	for _, it := range c.Items {
		t += it.LineTotal()
	}
	return Round(t)
}

// Violation explains one way a cart breaks the mandate.
type Violation struct {
	Code      string `json:"code"`
	Message   string `json:"message"`
	ProductID string `json:"product_id,omitempty"`
}

// Decision is the outcome of evaluating a cart against a mandate.
type Decision struct {
	Allowed          bool        `json:"allowed"`
	RequiresApproval bool        `json:"requires_approval"`
	Total            float64     `json:"total"`
	Currency         string      `json:"currency"`
	RemainingBudget  float64     `json:"remaining_budget"`
	RemainingAfter   float64     `json:"remaining_after"`
	Violations       []Violation `json:"violations"`
	Checks           []string    `json:"checks_passed"`
}

// Evaluate applies every rule of the mandate to the cart. It never mutates the
// mandate; callers reserve or spend budget separately once a payment happens.
func (m Mandate) Evaluate(cart Cart) Decision {
	d := Decision{Currency: m.Currency, RemainingBudget: m.Remaining(), Violations: []Violation{}, Checks: []string{}}
	if len(cart.Items) == 0 {
		d.Violations = append(d.Violations, Violation{Code: "empty_cart", Message: "the cart has no items"})
	}
	if cart.Currency != "" && !strings.EqualFold(cart.Currency, m.Currency) {
		d.Violations = append(d.Violations, Violation{Code: "currency_mismatch", Message: fmt.Sprintf("cart is in %s but the mandate is in %s", cart.Currency, m.Currency)})
	}
	allowed := map[string]bool{}
	for _, c := range m.AllowedCategories {
		allowed[strings.ToLower(strings.TrimSpace(c))] = true
	}
	for _, it := range cart.Items {
		if it.Quantity <= 0 {
			d.Violations = append(d.Violations, Violation{Code: "bad_quantity", Message: fmt.Sprintf("%q has quantity %d", it.Title, it.Quantity), ProductID: it.ProductID})
		}
		if it.Currency != "" && !strings.EqualFold(it.Currency, m.Currency) {
			d.Violations = append(d.Violations, Violation{Code: "currency_mismatch", Message: fmt.Sprintf("%q is priced in %s, mandate is %s", it.Title, it.Currency, m.Currency), ProductID: it.ProductID})
		}
		if m.MaxPerItem > 0 && it.UnitPrice > m.MaxPerItem+1e-9 {
			d.Violations = append(d.Violations, Violation{Code: "item_over_cap", Message: fmt.Sprintf("%q costs %.2f, above the per-item cap of %.2f", it.Title, it.UnitPrice, m.MaxPerItem), ProductID: it.ProductID})
		}
		if len(allowed) > 0 && !allowed[strings.ToLower(it.Category)] {
			d.Violations = append(d.Violations, Violation{Code: "category_not_allowed", Message: fmt.Sprintf("%q is in category %q which this mandate does not allow (allowed: %s)", it.Title, it.Category, strings.Join(m.AllowedCategories, ", ")), ProductID: it.ProductID})
		}
		if MerchantBlocked(it.Merchant, m.BlockedMerchants) {
			d.Violations = append(d.Violations, Violation{Code: "merchant_blocked", Message: fmt.Sprintf("merchant %q is blocked by this mandate", it.Merchant), ProductID: it.ProductID})
		}
		if m.MinRating > 0 && it.Rating > 0 && it.Rating < m.MinRating {
			d.Violations = append(d.Violations, Violation{Code: "rating_too_low", Message: fmt.Sprintf("%q is rated %.1f, below the minimum %.1f", it.Title, it.Rating, m.MinRating), ProductID: it.ProductID})
		}
	}
	d.Total = cart.Total()
	d.RemainingAfter = Round(d.RemainingBudget - d.Total)
	if d.Total > d.RemainingBudget+1e-9 {
		d.Violations = append(d.Violations, Violation{Code: "over_budget", Message: fmt.Sprintf("cart total %.2f exceeds the remaining budget of %.2f", d.Total, d.RemainingBudget)})
	}
	d.Allowed = len(d.Violations) == 0
	if d.Allowed {
		d.Checks = append(d.Checks, "within_budget")
		if m.MaxPerItem > 0 {
			d.Checks = append(d.Checks, "under_per_item_cap")
		}
		if len(allowed) > 0 {
			d.Checks = append(d.Checks, "allowed_categories")
		}
		if len(m.BlockedMerchants) > 0 {
			d.Checks = append(d.Checks, "no_blocked_merchants")
		}
		if m.MinRating > 0 {
			d.Checks = append(d.Checks, "min_rating")
		}
	}
	d.RequiresApproval = m.RequireApproval || m.AutoApproveUnder <= 0 || d.Total >= m.AutoApproveUnder-1e-9
	return d
}

// Bounds on what a mandate may hold. Money is capped well below where
// float64 arithmetic (and JSON encoding of the results) stops being exact.
const (
	MaxMoney       = 1e9
	MaxNameLen     = 100
	MaxListEntries = 20
	MaxEntryLen    = 64
)

func finite(v float64) bool { return !math.IsNaN(v) && !math.IsInf(v, 0) }

// Validate checks the mandate itself is coherent and inside the bounds.
func (m Mandate) Validate() error {
	if strings.TrimSpace(m.Name) == "" {
		return fmt.Errorf("name is required")
	}
	if utf8.RuneCountInString(m.Name) > MaxNameLen {
		return fmt.Errorf("name must be at most %d characters", MaxNameLen)
	}
	for _, v := range []float64{m.TotalBudget, m.MaxPerItem, m.AutoApproveUnder, m.MinRating, m.Spent, m.Reserved} {
		if !finite(v) {
			return fmt.Errorf("amounts must be finite numbers")
		}
	}
	if m.TotalBudget <= 0 {
		return fmt.Errorf("total_budget must be positive")
	}
	if m.TotalBudget > MaxMoney || m.MaxPerItem > MaxMoney || m.AutoApproveUnder > MaxMoney {
		return fmt.Errorf("amounts cannot exceed %.0f", MaxMoney)
	}
	if m.MaxPerItem < 0 || m.AutoApproveUnder < 0 || m.MinRating < 0 {
		return fmt.Errorf("limits cannot be negative")
	}
	if m.MinRating > 5 {
		return fmt.Errorf("min_rating cannot exceed 5")
	}
	if len(m.Currency) != 3 {
		return fmt.Errorf("currency must be a 3-letter ISO code")
	}
	for name, list := range map[string][]string{"allowed_categories": m.AllowedCategories, "blocked_merchants": m.BlockedMerchants} {
		if len(list) > MaxListEntries {
			return fmt.Errorf("%s can hold at most %d entries", name, MaxListEntries)
		}
		for _, e := range list {
			if utf8.RuneCountInString(e) > MaxEntryLen {
				return fmt.Errorf("%s entries must be at most %d characters", name, MaxEntryLen)
			}
		}
	}
	return nil
}

// Clone returns a copy that shares no slice with the original.
func (m Mandate) Clone() Mandate {
	out := m
	out.AllowedCategories = append([]string(nil), m.AllowedCategories...)
	out.BlockedMerchants = append([]string(nil), m.BlockedMerchants...)
	if m.VaultedAt != nil {
		t := *m.VaultedAt
		out.VaultedAt = &t
	}
	return out
}

// Clone returns a copy of the cart that shares no slice with the original.
func (c Cart) Clone() Cart {
	out := c
	out.Items = append([]CartItem(nil), c.Items...)
	return out
}

// Clone returns a copy of the decision that shares no slice with the original.
func (d Decision) Clone() Decision {
	out := d
	out.Violations = append([]Violation(nil), d.Violations...)
	out.Checks = append([]string(nil), d.Checks...)
	return out
}

// Redacted returns the mandate as the API shows it: the saved PayPal token
// id stays on the server, callers only learn that one exists.
func (m Mandate) Redacted() Mandate {
	out := m
	out.AllowedCategories = append([]string(nil), m.AllowedCategories...)
	out.BlockedMerchants = append([]string(nil), m.BlockedMerchants...)
	out.HasSaved = m.VaultID != ""
	out.VaultID = ""
	return out
}

// Round rounds money to cents; a non-finite value is returned unchanged so
// it can be rejected rather than turned into a surprise.
func Round(v float64) float64 {
	if !finite(v) {
		return v
	}
	return math.Round(v*100) / 100
}

// merchantDomain normalises a merchant name or domain for the block list:
// lower case, no scheme, no leading "www.".
func merchantDomain(s string) string {
	s = strings.ToLower(strings.TrimSpace(s))
	s = strings.TrimPrefix(strings.TrimPrefix(s, "https://"), "http://")
	if i := strings.IndexAny(s, "/?#"); i >= 0 {
		s = s[:i]
	}
	return strings.TrimPrefix(s, "www.")
}

// MerchantBlocked reports whether a merchant matches an entry of the block
// list: the same domain, or a subdomain of it (shop.temu.com for temu.com).
func MerchantBlocked(merchant string, blocked []string) bool {
	m := merchantDomain(merchant)
	if m == "" {
		return false
	}
	for _, b := range blocked {
		b = merchantDomain(b)
		if b != "" && (m == b || strings.HasSuffix(m, "."+b)) {
			return true
		}
	}
	return false
}
