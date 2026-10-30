package catalog

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
	"unicode/utf8"
)

// Channel3 searches real products through the Channel3 product search API
// (https://docs.trychannel3.com, OpenAPI at https://api.trychannel3.com/openapi.json).
// Authentication is an API key in the x-api-key header; the free tier includes
// 1,000 searches per month. Every search costs one credit.
//
// Channel3 products carry one or more merchant offers. Mandate's cart needs a
// single merchant and price per item, so each product is normalised to its
// cheapest in-stock offer; the number of alternative offers is kept in the
// attributes so the agent can mention them.
type Channel3 struct {
	APIKey   string
	BaseURL  string
	HTTP     *http.Client
	Country  string // ISO 3166-1 alpha-2, default US
	Currency string // ISO 4217 used when a query does not say, default USD
}

// NewChannel3 builds a client. baseURL may be empty to use the public API.
func NewChannel3(apiKey, baseURL string) *Channel3 {
	if baseURL == "" {
		baseURL = "https://api.trychannel3.com"
	}
	return &Channel3{APIKey: apiKey, BaseURL: strings.TrimRight(baseURL, "/"), HTTP: &http.Client{Timeout: 25 * time.Second}, Country: "US", Currency: "USD"}
}

// Name implements Searcher.
func (c *Channel3) Name() string { return "channel3" }

// --- wire types (subset of the Channel3 OpenAPI schema) ---

type c3SearchRequest struct {
	Query   string           `json:"query"`
	Limit   int              `json:"limit,omitempty"`
	Filters *c3SearchFilters `json:"filters,omitempty"`
	Config  *c3SearchConfig  `json:"config,omitempty"`
}

type c3SearchFilters struct {
	Price        *c3PriceFilter `json:"price,omitempty"`
	Availability []string       `json:"availability,omitempty"`
	Conditions   []string       `json:"conditions,omitempty"`
}

type c3PriceFilter struct {
	MinPrice *float64 `json:"min_price,omitempty"`
	MaxPrice *float64 `json:"max_price,omitempty"`
}

type c3SearchConfig struct {
	Country  string `json:"country,omitempty"`
	Currency string `json:"currency,omitempty"`
	Mode     string `json:"mode,omitempty"`
}

type c3SearchResponse struct {
	Products      []c3Product `json:"products"`
	NextPageToken *string     `json:"next_page_token"`
}

type c3Product struct {
	ID          string   `json:"id"`
	Title       string   `json:"title"`
	Description *string  `json:"description"`
	Brands      []c3Name `json:"brands"`
	Images      []struct {
		URL         string  `json:"url"`
		CleanedURL  *string `json:"cleaned_url"`
		IsMainImage bool    `json:"is_main_image"`
	} `json:"images"`
	Category *struct {
		Slug  string   `json:"slug"`
		Title string   `json:"title"`
		Path  []c3Name `json:"path"`
	} `json:"category"`
	Materials           []string            `json:"materials"`
	KeyFeatures         []string            `json:"key_features"`
	Offers              []c3Offer           `json:"offers"`
	StructuredAttribute map[string][]string `json:"structured_attributes"`
	Ratings             *struct {
		Value float64 `json:"value"`
		Count int     `json:"count"`
	} `json:"ratings"`
}

type c3Name struct {
	ID    string `json:"id,omitempty"`
	Slug  string `json:"slug,omitempty"`
	Name  string `json:"name,omitempty"`
	Title string `json:"title,omitempty"`
}

type c3Offer struct {
	URL    string `json:"url"`
	Domain string `json:"domain"`
	Price  struct {
		Price          float64  `json:"price"`
		CompareAtPrice *float64 `json:"compare_at_price"`
		Currency       string   `json:"currency"`
	} `json:"price"`
	Availability string  `json:"availability"`
	Condition    *string `json:"condition"`
}

// Search implements Searcher using POST /v1/search.
func (c *Channel3) Search(ctx context.Context, q Query) ([]Product, error) {
	limit := q.Limit
	if limit <= 0 {
		limit = 10
	}
	if limit > 30 {
		limit = 30
	}
	cur := strings.ToUpper(q.Currency)
	if cur == "" {
		cur = c.Currency
	}
	req := c3SearchRequest{
		Query:   q.Text,
		Limit:   limit,
		Filters: &c3SearchFilters{Availability: []string{"InStock"}, Conditions: []string{"new"}},
		Config:  &c3SearchConfig{Country: c.Country, Currency: cur, Mode: "default"},
	}
	if q.MaxPrice > 0 {
		mp := q.MaxPrice
		req.Filters.Price = &c3PriceFilter{MaxPrice: &mp}
	}
	raw, err := c.do(ctx, http.MethodPost, "/v1/search", req)
	if err != nil {
		return nil, err
	}
	var resp c3SearchResponse
	if err := json.Unmarshal(raw, &resp); err != nil {
		return nil, fmt.Errorf("channel3: decode search: %w", err)
	}
	out := make([]Product, 0, len(resp.Products))
	for _, cp := range resp.Products {
		p, ok := c.normalise(cp, cur)
		if !ok {
			continue
		}
		if q.MaxPrice > 0 && p.Price > q.MaxPrice {
			continue
		}
		if q.Category != "" && p.Category != NormaliseCategory(q.Category) {
			continue
		}
		out = append(out, p)
	}
	return out, nil
}

// Get implements Searcher using GET /v1/products/{id}.
func (c *Channel3) Get(ctx context.Context, id string) (*Product, error) {
	path := "/v1/products/" + url.PathEscape(id) + "?country=" + url.QueryEscape(c.Country) + "&currency=" + url.QueryEscape(c.Currency)
	raw, err := c.do(ctx, http.MethodGet, path, nil)
	if err != nil {
		return nil, err
	}
	var cp c3Product
	if err := json.Unmarshal(raw, &cp); err != nil {
		return nil, fmt.Errorf("channel3: decode product: %w", err)
	}
	// Same selection rules as Search (currency, in stock, new), so the cart
	// item is priced from the offer the agent compared.
	p, ok := c.normalise(cp, c.Currency)
	if !ok {
		return nil, ErrNotFound
	}
	return &p, nil
}

// normalise converts a Channel3 product into Mandate's Product using its
// cheapest in-stock offer. Products without a usable offer are skipped.
func (c *Channel3) normalise(cp c3Product, wantCurrency string) (Product, bool) {
	if cp.ID == "" || cp.Title == "" {
		return Product{}, false
	}
	var best *c3Offer
	inStock := 0
	usable := func(o *c3Offer) bool {
		if o.Price.Price <= 0 {
			return false
		}
		if wantCurrency != "" && o.Price.Currency != "" && !strings.EqualFold(o.Price.Currency, wantCurrency) {
			return false
		}
		return o.Availability == "" || o.Availability == "InStock"
	}
	isNew := func(o *c3Offer) bool { return o.Condition == nil || *o.Condition == "" || *o.Condition == "new" }
	// Prefer new offers; fall back to used only when nothing new is in stock.
	for pass := 0; pass < 2 && best == nil; pass++ {
		inStock = 0
		for i := range cp.Offers {
			o := &cp.Offers[i]
			if !usable(o) || (pass == 0 && !isNew(o)) {
				continue
			}
			inStock++
			if best == nil || o.Price.Price < best.Price.Price {
				best = o
			}
		}
	}
	if best == nil {
		return Product{}, false
	}
	p := Product{ID: cp.ID, Title: cp.Title, Source: "channel3", Attributes: map[string]string{}}
	p.Price = best.Price.Price
	p.Currency = strings.ToUpper(best.Price.Currency)
	if p.Currency == "" {
		p.Currency = "USD"
	}
	p.Merchant = best.Domain
	if p.Merchant == "" {
		p.Merchant = "unknown"
	}
	p.URL = best.URL
	if len(cp.Brands) > 0 {
		p.Brand = cp.Brands[0].Name
	}
	if cp.Description != nil {
		p.Description = truncate(*cp.Description, 400)
	}
	for _, img := range cp.Images {
		if img.IsMainImage || p.ImageURL == "" {
			p.ImageURL = img.URL
			if img.CleanedURL != nil && *img.CleanedURL != "" {
				p.ImageURL = *img.CleanedURL
			}
			if img.IsMainImage {
				break
			}
		}
	}
	if cp.Ratings != nil && cp.Ratings.Value > 0 {
		p.Rating = cp.Ratings.Value
		p.Reviews = cp.Ratings.Count
	}
	rawCat := ""
	if cp.Category != nil {
		parts := make([]string, 0, len(cp.Category.Path)+1)
		for _, n := range cp.Category.Path {
			parts = append(parts, n.Title)
		}
		if len(parts) == 0 {
			parts = append(parts, cp.Category.Title)
		}
		rawCat = strings.Join(parts, " > ")
		p.Attributes["raw_category"] = rawCat
		p.Attributes["category_slug"] = cp.Category.Slug
	}
	p.Category = NormaliseCategory(rawCat + " " + cp.Title)
	for k, vs := range cp.StructuredAttribute {
		if len(vs) > 0 {
			p.Attributes[k] = strings.Join(vs, ", ")
		}
	}
	if len(cp.KeyFeatures) > 0 {
		p.Attributes["key_features"] = truncate(strings.Join(cp.KeyFeatures, "; "), 300)
	}
	if len(cp.Materials) > 0 {
		p.Attributes["materials"] = strings.Join(cp.Materials, ", ")
	}
	if best.Price.CompareAtPrice != nil && *best.Price.CompareAtPrice > best.Price.Price {
		p.Attributes["compare_at_price"] = fmt.Sprintf("%.2f", *best.Price.CompareAtPrice)
	}
	if best.Condition != nil {
		p.Attributes["condition"] = *best.Condition
	}
	if inStock > 1 {
		p.Attributes["other_offers"] = fmt.Sprintf("%d more merchants", inStock-1)
	}
	return p, true
}

func (c *Channel3) do(ctx context.Context, method, path string, body any) (json.RawMessage, error) {
	var rdr io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return nil, err
		}
		rdr = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, c.BaseURL+path, rdr)
	if err != nil {
		return nil, err
	}
	req.Header.Set("x-api-key", c.APIKey)
	req.Header.Set("Accept", "application/json")
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return nil, fmt.Errorf("channel3: %w", err)
	}
	defer resp.Body.Close()
	data, _ := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	switch resp.StatusCode {
	case http.StatusNotFound:
		return nil, ErrNotFound
	case http.StatusUnauthorized:
		return nil, fmt.Errorf("channel3: API key rejected (401): check CHANNEL3_API_KEY")
	case http.StatusPaymentRequired:
		return nil, fmt.Errorf("channel3: credits exhausted (402): %s", detail(data))
	case http.StatusTooManyRequests:
		return nil, fmt.Errorf("channel3: rate limited (429), retry after %s", resp.Header.Get("Retry-After"))
	}
	if resp.StatusCode >= 300 {
		return nil, fmt.Errorf("channel3: %s %s -> %d: %s", method, path, resp.StatusCode, truncate(detail(data), 300))
	}
	return json.RawMessage(data), nil
}

func detail(data []byte) string {
	var e struct {
		Detail any `json:"detail"`
	}
	if json.Unmarshal(data, &e) == nil && e.Detail != nil {
		if s, ok := e.Detail.(string); ok {
			return s
		}
		b, _ := json.Marshal(e.Detail)
		return string(b)
	}
	return string(data)
}

func truncate(s string, n int) string {
	if utf8.RuneCountInString(s) <= n {
		return s
	}
	r := []rune(s)
	return string(r[:n]) + "…"
}
