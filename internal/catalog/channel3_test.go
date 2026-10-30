package catalog

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
)

// sample mirrors the Channel3 OpenAPI Product schema.
const c3Sample = `{"products":[
 {"id":"prod_kb1","title":"Keychron K2 Pro Mechanical Keyboard","description":"Brown switches, wireless.",
  "brands":[{"id":"b1","name":"Keychron"}],
  "images":[{"url":"https://img/x.jpg","cleaned_url":null,"is_main_image":false},{"url":"https://img/main.jpg","cleaned_url":"https://cdn/main.webp","is_main_image":true}],
  "category":{"slug":"keyboards","title":"Keyboards","path":[{"slug":"electronics","title":"Electronics"},{"slug":"computer-accessories","title":"Computer Accessories"},{"slug":"keyboards","title":"Keyboards"}],"has_children":false},
  "key_features":["Hot-swappable","Bluetooth 5.1"],
  "offers":[
    {"url":"https://buy/1","domain":"keychron.com","price":{"price":109.00,"compare_at_price":129.00,"currency":"USD"},"availability":"InStock","condition":"new"},
    {"url":"https://buy/2","domain":"amazon.com","price":{"price":99.99,"compare_at_price":null,"currency":"USD"},"availability":"InStock","condition":"new"},
    {"url":"https://buy/3","domain":"cheap.com","price":{"price":59.00,"currency":"USD"},"availability":"OutOfStock"}
  ],
  "structured_attributes":{"color":["black"],"switch-type":["brown"]},
  "ratings":{"value":4.6,"count":2140,"source_count":2}},
 {"id":"prod_nooffer","title":"Ghost product","offers":[]},
 {"id":"prod_gbp","title":"UK only keyboard","offers":[{"url":"u","domain":"uk.com","price":{"price":80,"currency":"GBP"},"availability":"InStock"}]}
],"next_page_token":null}`

func TestChannel3SearchDecodesOpenAPIShape(t *testing.T) {
	var gotBody map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("x-api-key") != "key123" {
			http.Error(w, `{"detail":"Unauthorized"}`, 401)
			return
		}
		switch {
		case r.Method == "POST" && r.URL.Path == "/v1/search":
			b, _ := io.ReadAll(r.Body)
			_ = json.Unmarshal(b, &gotBody)
			_, _ = io.WriteString(w, c3Sample)
		case r.Method == "GET" && r.URL.Path == "/v1/products/prod_kb1":
			var resp map[string]any
			_ = json.Unmarshal([]byte(c3Sample), &resp)
			_ = json.NewEncoder(w).Encode(resp["products"].([]any)[0])
		case r.Method == "GET" && r.URL.Path == "/v1/products/nope":
			http.Error(w, `{"detail":"Product not found"}`, 404)
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	c := NewChannel3("key123", srv.URL)
	res, err := c.Search(context.Background(), Query{Text: "mechanical keyboard brown switches", MaxPrice: 120, Limit: 8, Currency: "usd"})
	if err != nil {
		t.Fatal(err)
	}
	if gotBody["query"] != "mechanical keyboard brown switches" || gotBody["limit"].(float64) != 8 {
		t.Fatalf("request body = %v", gotBody)
	}
	if gotBody["filters"].(map[string]any)["price"].(map[string]any)["max_price"].(float64) != 120 {
		t.Fatalf("price filter missing: %v", gotBody["filters"])
	}
	if gotBody["config"].(map[string]any)["currency"] != "USD" {
		t.Fatalf("config = %v", gotBody["config"])
	}
	if len(res) != 1 {
		t.Fatalf("expected 1 usable product (no-offer and GBP products dropped), got %d: %+v", len(res), res)
	}
	p := res[0]
	if p.ID != "prod_kb1" || p.Price != 99.99 || p.Merchant != "amazon.com" || p.URL != "https://buy/2" {
		t.Fatalf("cheapest in-stock offer not chosen: %+v", p)
	}
	if p.Brand != "Keychron" || p.Rating != 4.6 || p.Reviews != 2140 || p.Currency != "USD" {
		t.Fatalf("brand/rating wrong: %+v", p)
	}
	if p.Category != "computers" {
		t.Fatalf("category = %q (raw %q)", p.Category, p.Attributes["raw_category"])
	}
	if p.ImageURL != "https://cdn/main.webp" {
		t.Fatalf("image = %q", p.ImageURL)
	}
	if p.Attributes["switch-type"] != "brown" || p.Attributes["other_offers"] != "1 more merchants" || p.Attributes["key_features"] == "" {
		t.Fatalf("attributes = %v", p.Attributes)
	}
	got, err := c.Get(context.Background(), "prod_kb1")
	if err != nil || got.ID != "prod_kb1" {
		t.Fatalf("get: %v %+v", err, got)
	}
	if _, err := c.Get(context.Background(), "nope"); err != ErrNotFound {
		t.Fatalf("expected ErrNotFound, got %v", err)
	}
	bad := NewChannel3("wrong", srv.URL)
	if _, err := bad.Search(context.Background(), Query{Text: "x"}); err == nil {
		t.Fatal("expected auth error")
	}
}

func TestChannel3CategoryComesFromThePathNotTheTitle(t *testing.T) {
	c := &Channel3{}
	kettle := c3Product{ID: "k1", Title: "Electric gooseneck kettle with USB charging port and keyboard-style buttons"}
	kettle.Category = &struct {
		Slug  string   `json:"slug"`
		Title string   `json:"title"`
		Path  []c3Name `json:"path"`
	}{Slug: "kettles", Title: "Kettles", Path: []c3Name{{Title: "Home"}, {Title: "Kitchen"}, {Title: "Kettles"}}}
	kettle.Offers = []c3Offer{{URL: "https://shop.example/k1", Domain: "shop.example"}}
	kettle.Offers[0].Price.Price = 59
	kettle.Offers[0].Price.Currency = "USD"
	p, ok := c.normalise(kettle, "USD")
	if !ok || p.Category != "kitchen" {
		t.Fatalf("a kettle must stay in the kitchen whatever its title says: ok=%v category=%q", ok, p.Category)
	}
	bare := kettle
	bare.Category = nil
	if p, ok := c.normalise(bare, "USD"); !ok || p.Category != "other" {
		t.Fatalf("no category path means other, never a guess from the title: %q", p.Category)
	}
}
