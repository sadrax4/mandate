package server

import (
	"net/http"
	"sort"
	"time"

	"mandate/internal/mandate"
	"mandate/internal/store"
)

// Insights aggregates the ledger for the spending dashboard: every purchased
// line item, plus totals by mandate, category, merchant and day. Only
// captured payments count as spend; pending and cancelled orders are listed
// but excluded from the totals.

type insightItem struct {
	OrderID   string    `json:"order_id"`
	CaptureID string    `json:"capture_id,omitempty"`
	MissionID string    `json:"mission_id"`
	Mandate   string    `json:"mandate"`
	Goal      string    `json:"goal"`
	Product   string    `json:"product"`
	ProductID string    `json:"product_id"`
	Merchant  string    `json:"merchant"`
	Category  string    `json:"category"`
	UnitPrice float64   `json:"unit_price"`
	Quantity  int       `json:"quantity"`
	Line      float64   `json:"line_total"`
	Currency  string    `json:"currency"`
	Status    string    `json:"status"`
	PaidVia   string    `json:"paid_via"`
	Payer     string    `json:"payer,omitempty"`
	Mode      string    `json:"mode"`
	CreatedAt time.Time `json:"created_at"`
}

type insightBucket struct {
	Key    string  `json:"key"`
	Amount float64 `json:"amount"`
	Count  int     `json:"count"`
}

func (s *Server) handleInsights(w http.ResponseWriter, _ *http.Request) {
	mandates := s.st.ListMandates()
	mdByID := map[string]*mandate.Mandate{}
	for _, m := range mandates {
		mdByID[m.ID] = m
	}
	type mdRow struct {
		ID        string  `json:"id"`
		Name      string  `json:"name"`
		Currency  string  `json:"currency"`
		Budget    float64 `json:"budget"`
		Spent     float64 `json:"spent"`
		Reserved  float64 `json:"reserved"`
		Remaining float64 `json:"remaining"`
		Orders    int     `json:"orders"`
		Items     int     `json:"items"`
		Saved     bool    `json:"saved_paypal"`
	}
	mdRows := map[string]*mdRow{}
	for _, m := range mandates {
		mdRows[m.ID] = &mdRow{ID: m.ID, Name: m.Name, Currency: m.Currency, Budget: m.TotalBudget, Spent: m.Spent, Reserved: m.Reserved, Remaining: m.Remaining(), Saved: m.HasSavedPayment()}
	}

	byCat, byMerchant, byDay := map[string]*insightBucket{}, map[string]*insightBucket{}, map[string]*insightBucket{}
	add := func(m map[string]*insightBucket, key string, amount float64) {
		b := m[key]
		if b == nil {
			b = &insightBucket{Key: key}
			m[key] = b
		}
		b.Amount = mandate.Round(b.Amount + amount)
		b.Count++
	}
	var items []insightItem
	captured, orders, lines := 0.0, 0, 0
	for _, p := range s.st.ListPayments() {
		md := mdByID[p.MandateID]
		mdName := ""
		if md != nil {
			mdName = md.Name
		}
		goal := ""
		if ms, err := s.st.GetMission(p.MissionID); err == nil {
			goal = ms.Goal
		}
		via := "PayPal checkout"
		if p.Vaulted {
			via = "saved PayPal"
		}
		completed := p.Status == store.PaymentCompleted || p.Status == store.PaymentPartiallyRefunded
		net := mandate.Round(p.Amount - p.Refunded)
		if completed {
			captured = mandate.Round(captured + net)
			orders++
			if r := mdRows[p.MandateID]; r != nil {
				r.Orders++
			}
			add(byDay, p.CreatedAt.UTC().Format("2006-01-02"), net)
		}
		pr, err := s.st.GetProposal(p.ProposalID)
		if err != nil {
			continue
		}
		for _, it := range pr.Cart.Items {
			q := it.Quantity
			if q <= 0 {
				q = 1
			}
			line := mandate.Round(it.UnitPrice * float64(q))
			items = append(items, insightItem{OrderID: p.ID, CaptureID: p.CaptureID, MissionID: p.MissionID, Mandate: mdName, Goal: goal, Product: it.Title, ProductID: it.ProductID,
				Merchant: it.Merchant, Category: it.Category, UnitPrice: it.UnitPrice, Quantity: q, Line: line, Currency: p.Currency, Status: p.Status, PaidVia: via, Payer: p.PayerEmail, Mode: p.Mode, CreatedAt: p.CreatedAt})
			if completed {
				lines++
				add(byCat, it.Category, line)
				add(byMerchant, it.Merchant, line)
				if r := mdRows[p.MandateID]; r != nil {
					r.Items++
				}
			}
		}
	}
	sort.Slice(items, func(i, j int) bool { return items[i].CreatedAt.After(items[j].CreatedAt) })
	flat := func(m map[string]*insightBucket, byKey bool) []insightBucket {
		out := make([]insightBucket, 0, len(m))
		for _, b := range m {
			out = append(out, *b)
		}
		sort.Slice(out, func(i, j int) bool {
			if byKey {
				return out[i].Key < out[j].Key
			}
			if out[i].Amount != out[j].Amount {
				return out[i].Amount > out[j].Amount
			}
			return out[i].Key < out[j].Key
		})
		return out
	}
	mds := make([]mdRow, 0, len(mdRows))
	for _, m := range mandates {
		mds = append(mds, *mdRows[m.ID])
	}
	avg := 0.0
	if orders > 0 {
		avg = mandate.Round(captured / float64(orders))
	}
	writeJSON(w, 200, map[string]any{
		"currency":    s.cfg.Currency,
		"totals":      map[string]any{"captured": captured, "orders": orders, "items": lines, "mandates": len(mandates), "average_order": avg},
		"mandates":    mds,
		"by_category": flat(byCat, false),
		"by_merchant": flat(byMerchant, false),
		"by_day":      flat(byDay, true),
		"items":       items,
	})
}
