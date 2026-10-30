package catalog

import (
	"context"
	"sort"
	"strings"
	"sync"
)

// Mock is an in-memory demo catalog. It is deterministic, works offline and
// is what the automated tests and the "offline demo" mode run against.
type Mock struct {
	mu       sync.RWMutex
	products []Product
}

// NewMock returns a catalog seeded with the built-in demo inventory.
func NewMock() *Mock {
	m := &Mock{}
	m.products = append(m.products, demoProducts...)
	return m
}

// Name implements Searcher.
func (m *Mock) Name() string { return "mock" }

// Add inserts extra products (used by tests).
func (m *Mock) Add(p ...Product) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.products = append(m.products, p...)
}

// Get implements Searcher.
func (m *Mock) Get(_ context.Context, id string) (*Product, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	for i := range m.products {
		if m.products[i].ID == id {
			p := m.products[i]
			return &p, nil
		}
	}
	return nil, ErrNotFound
}

// Search implements Searcher with a small keyword scorer: every query token
// that appears in the title, brand, category, description or attributes adds
// to the score; exact title-token hits weigh more. Results are filtered by
// price/category and sorted by score, then rating.
func (m *Mock) Search(_ context.Context, q Query) ([]Product, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()

	tokens := tokenize(q.Text)
	wantCat := ""
	if q.Category != "" {
		wantCat = NormaliseCategory(q.Category)
	}
	type scored struct {
		p     Product
		score float64
	}
	var out []scored
	for _, p := range m.products {
		if q.MaxPrice > 0 && p.Price > q.MaxPrice {
			continue
		}
		if wantCat != "" && p.Category != wantCat {
			continue
		}
		score := 0.0
		if len(tokens) == 0 {
			score = 1
		}
		titleToks := tokenize(p.Title)
		hay := strings.ToLower(p.Title + " " + p.Brand + " " + p.Category + " " + p.Description + " " + p.Merchant)
		for _, kv := range p.Attributes {
			hay += " " + strings.ToLower(kv)
		}
		for _, t := range tokens {
			if containsToken(titleToks, t) {
				score += 3
			} else if strings.Contains(hay, t) {
				score += 1
			}
		}
		if score == 0 {
			continue
		}
		out = append(out, scored{p, score})
	}
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].score != out[j].score {
			return out[i].score > out[j].score
		}
		if out[i].p.Rating != out[j].p.Rating {
			return out[i].p.Rating > out[j].p.Rating
		}
		return out[i].p.Price < out[j].p.Price
	})
	limit := q.Limit
	if limit <= 0 || limit > 25 {
		limit = 10
	}
	res := make([]Product, 0, limit)
	for i := 0; i < len(out) && i < limit; i++ {
		res = append(res, out[i].p)
	}
	return res, nil
}

func tokenize(s string) []string {
	s = strings.ToLower(s)
	fields := strings.FieldsFunc(s, func(r rune) bool {
		return !(r >= 'a' && r <= 'z' || r >= '0' && r <= '9' || r == '-')
	})
	var out []string
	for _, f := range fields {
		f = strings.Trim(f, "-")
		if len(f) < 2 {
			continue
		}
		switch f {
		case "the", "and", "for", "with", "under", "a", "an", "of", "to", "in", "on", "buy", "me", "some", "good", "best", "need", "want":
			continue
		}
		// crude singularisation so "keyboards" matches "keyboard"
		if strings.HasSuffix(f, "s") && len(f) > 3 && !strings.HasSuffix(f, "ss") {
			f = strings.TrimSuffix(f, "s")
		}
		out = append(out, f)
	}
	return out
}

func containsToken(toks []string, t string) bool {
	for _, x := range toks {
		if x == t || strings.HasPrefix(x, t) || strings.HasPrefix(t, x) && len(x) >= 4 {
			return true
		}
	}
	return false
}
