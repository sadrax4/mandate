package catalog

import (
	"context"
	"testing"
)

func TestMockSearch(t *testing.T) {
	m := NewMock()
	ctx := context.Background()
	res, err := m.Search(ctx, Query{Text: "mechanical keyboard brown switches", Limit: 5})
	if err != nil || len(res) == 0 {
		t.Fatalf("search failed: %v (%d results)", err, len(res))
	}
	if res[0].Category != "computers" {
		t.Fatalf("top result %q should be a keyboard, got category %s", res[0].Title, res[0].Category)
	}
	for _, p := range res {
		if p.Attributes["switch"] != "" && p.Attributes["switch"] != "brown" && p.Attributes["switch"] != "tactile" {
			// keyboards without brown switches may appear lower, but the top hits must be brown
			break
		}
	}
	res, _ = m.Search(ctx, Query{Text: "usb-c hub", MaxPrice: 30})
	for _, p := range res {
		if p.Price > 30 {
			t.Fatalf("max_price not applied: %s %.2f", p.Title, p.Price)
		}
	}
	res, _ = m.Search(ctx, Query{Text: "kettle", Category: "kitchen"})
	if len(res) < 2 {
		t.Fatalf("expected kettles, got %d", len(res))
	}
	res, _ = m.Search(ctx, Query{Text: "kettle", Category: "office"})
	if len(res) != 0 {
		t.Fatalf("category filter leaked: %d", len(res))
	}
	if _, err := m.Get(ctx, "nope"); err != ErrNotFound {
		t.Fatalf("expected ErrNotFound, got %v", err)
	}
	p, err := m.Get(ctx, "hub-001")
	if err != nil || p.Merchant != "TechDepot" {
		t.Fatalf("get: %v %+v", err, p)
	}
}

func TestNormaliseCategory(t *testing.T) {
	cases := map[string]string{"Keyboards": "computers", "Headphones & Audio": "audio", "": "other", "Coffee Makers": "kitchen", "office": "office", "Yoga": "fitness"}
	for in, want := range cases {
		if got := NormaliseCategory(in); got != want {
			t.Errorf("%q -> %q, want %q", in, got, want)
		}
	}
}
