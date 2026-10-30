package catalog

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"
)

// Channel3 searches real products through the Channel3 product search API
// (https://docs.trychannel3.com). Authentication is an API key sent in the
// x-api-key header; the free tier includes 1,000 searches per month.
//
// The decoder is deliberately tolerant: Channel3's schema is read from the
// OpenAPI document at https://api.trychannel3.com/openapi.json and the field
// names below cover the documented shapes plus common aliases.
type Channel3 struct {
	APIKey  string
	BaseURL string
	HTTP    *http.Client
}

// NewChannel3 builds a client. baseURL may be empty to use the public API.
func NewChannel3(apiKey, baseURL string) *Channel3 {
	if baseURL == "" {
		baseURL = "https://api.trychannel3.com/v1"
	}
	return &Channel3{APIKey: apiKey, BaseURL: strings.TrimRight(baseURL, "/"), HTTP: &http.Client{Timeout: 20 * time.Second}}
}

// Name implements Searcher.
func (c *Channel3) Name() string { return "channel3" }

// Search implements Searcher.
func (c *Channel3) Search(ctx context.Context, q Query) ([]Product, error) {
	limit := q.Limit
	if limit <= 0 || limit > 25 {
		limit = 10
	}
	body := map[string]any{"query": q.Text, "limit": limit}
	if q.MaxPrice > 0 {
		body["filters"] = map[string]any{"price": map[string]any{"max_price": q.MaxPrice}}
	}
	raw, err := c.do(ctx, http.MethodPost, "/search", body)
	if err != nil {
		return nil, err
	}
	items := extractArray(raw, "products", "results", "items", "data")
	out := make([]Product, 0, len(items))
	for _, it := range items {
		p := decodeProduct(it)
		if p.ID == "" || p.Title == "" {
			continue
		}
		if q.MaxPrice > 0 && p.Price > q.MaxPrice {
			continue
		}
		out = append(out, p)
	}
	return out, nil
}

// Get implements Searcher.
func (c *Channel3) Get(ctx context.Context, id string) (*Product, error) {
	raw, err := c.do(ctx, http.MethodGet, "/products/"+id, nil)
	if err != nil {
		return nil, err
	}
	var m map[string]any
	if err := json.Unmarshal(raw, &m); err != nil {
		return nil, fmt.Errorf("channel3: decode product: %w", err)
	}
	if inner, ok := m["product"].(map[string]any); ok {
		m = inner
	}
	p := decodeProduct(m)
	if p.ID == "" {
		return nil, ErrNotFound
	}
	return &p, nil
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
	data, _ := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if resp.StatusCode == http.StatusNotFound {
		return nil, ErrNotFound
	}
	if resp.StatusCode == http.StatusTooManyRequests {
		return nil, fmt.Errorf("channel3: rate limited (retry after %s)", resp.Header.Get("Retry-After"))
	}
	if resp.StatusCode >= 300 {
		return nil, fmt.Errorf("channel3: %s %s -> %d: %s", method, path, resp.StatusCode, truncate(string(data), 300))
	}
	return json.RawMessage(data), nil
}

func extractArray(raw json.RawMessage, keys ...string) []map[string]any {
	var arr []map[string]any
	if err := json.Unmarshal(raw, &arr); err == nil {
		return arr
	}
	var obj map[string]any
	if err := json.Unmarshal(raw, &obj); err != nil {
		return nil
	}
	for _, k := range keys {
		if v, ok := obj[k].([]any); ok {
			out := make([]map[string]any, 0, len(v))
			for _, e := range v {
				if m, ok := e.(map[string]any); ok {
					out = append(out, m)
				}
			}
			return out
		}
	}
	return nil
}

func decodeProduct(m map[string]any) Product {
	p := Product{Source: "channel3", Currency: "USD", Attributes: map[string]string{}}
	p.ID = str(m, "id", "product_id", "sku")
	p.Title = str(m, "title", "name", "product_name")
	p.Brand = str(m, "brand_name", "brand")
	if b, ok := m["brand"].(map[string]any); ok {
		p.Brand = str(b, "name")
	}
	p.Merchant = str(m, "merchant_name", "merchant", "seller", "retailer", "store")
	if mm, ok := m["merchant"].(map[string]any); ok {
		p.Merchant = str(mm, "name")
	}
	if p.Merchant == "" {
		p.Merchant = p.Brand
	}
	if p.Merchant == "" {
		p.Merchant = "unknown"
	}
	p.URL = str(m, "url", "product_url", "link")
	p.ImageURL = str(m, "image_url", "image", "thumbnail")
	if imgs, ok := m["image_urls"].([]any); ok && len(imgs) > 0 {
		if s, ok := imgs[0].(string); ok {
			p.ImageURL = s
		}
	}
	p.Description = truncate(str(m, "description", "summary"), 400)
	// price: number, string or object
	switch v := m["price"].(type) {
	case float64:
		p.Price = v
	case string:
		p.Price, _ = strconv.ParseFloat(strings.TrimLeft(v, "$"), 64)
	case map[string]any:
		p.Price = num(v, "price", "amount", "value", "current", "current_price")
		if cur := str(v, "currency", "currency_code"); cur != "" {
			p.Currency = cur
		}
	}
	if p.Price == 0 {
		p.Price = num(m, "current_price", "sale_price", "amount")
	}
	if cur := str(m, "currency", "currency_code"); cur != "" {
		p.Currency = cur
	}
	p.Rating = num(m, "rating", "average_rating", "stars")
	p.Reviews = int(num(m, "review_count", "reviews", "rating_count", "num_reviews"))
	cat := str(m, "category", "category_name", "product_type")
	if cs, ok := m["categories"].([]any); ok && len(cs) > 0 {
		if s, ok := cs[0].(string); ok {
			cat = s
		}
	}
	p.Category = NormaliseCategory(cat)
	if cat != "" {
		p.Attributes["raw_category"] = cat
	}
	if attrs, ok := m["attributes"].(map[string]any); ok {
		for k, v := range attrs {
			p.Attributes[k] = fmt.Sprint(v)
		}
	}
	if avail := str(m, "availability"); avail != "" {
		p.Attributes["availability"] = avail
	}
	return p
}

func str(m map[string]any, keys ...string) string {
	for _, k := range keys {
		if v, ok := m[k]; ok {
			switch s := v.(type) {
			case string:
				if s != "" {
					return s
				}
			case float64:
				return strconv.FormatFloat(s, 'f', -1, 64)
			}
		}
	}
	return ""
}

func num(m map[string]any, keys ...string) float64 {
	for _, k := range keys {
		if v, ok := m[k]; ok {
			switch n := v.(type) {
			case float64:
				return n
			case string:
				if f, err := strconv.ParseFloat(strings.TrimLeft(n, "$"), 64); err == nil {
					return f
				}
			}
		}
	}
	return 0
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}
