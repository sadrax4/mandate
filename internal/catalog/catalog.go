// Package catalog provides product search for the shopping agent.
//
// Two implementations ship with Mandate: a built-in demo catalog (Mock) that
// works offline, and a Channel3 client (the product search API built for AI
// agents). Both satisfy Searcher so the agent code never cares which one is
// behind it.
package catalog

import (
	"context"
	"errors"
	"strings"
)

// Product is a normalised view of a purchasable item.
type Product struct {
	ID          string            `json:"id"`
	Title       string            `json:"title"`
	Brand       string            `json:"brand,omitempty"`
	Merchant    string            `json:"merchant"`
	Category    string            `json:"category"`
	Price       float64           `json:"price"`
	Currency    string            `json:"currency"`
	Rating      float64           `json:"rating,omitempty"`
	Reviews     int               `json:"reviews,omitempty"`
	URL         string            `json:"url,omitempty"`
	ImageURL    string            `json:"image_url,omitempty"`
	Description string            `json:"description,omitempty"`
	Attributes  map[string]string `json:"attributes,omitempty"`
	Source      string            `json:"source"`
}

// Query is a product search request.
type Query struct {
	Text     string  `json:"query"`
	MaxPrice float64 `json:"max_price,omitempty"`
	Category string  `json:"category,omitempty"`
	Limit    int     `json:"limit,omitempty"`
}

// Searcher is implemented by every catalog backend.
type Searcher interface {
	// Search returns products ranked by relevance for the query.
	Search(ctx context.Context, q Query) ([]Product, error)
	// Get returns a single product by ID.
	Get(ctx context.Context, id string) (*Product, error)
	// Name identifies the backend ("mock", "channel3").
	Name() string
}

// ErrNotFound is returned by Get when the product does not exist.
var ErrNotFound = errors.New("catalog: product not found")

// Categories lists the categories the demo catalog understands. Channel3
// results are mapped onto these where possible so mandate rules stay simple.
var Categories = []string{
	"electronics", "computers", "audio", "home", "kitchen", "office",
	"fitness", "clothing", "books", "toys", "other",
}

// NormaliseCategory maps free-form category text onto one of Categories.
func NormaliseCategory(s string) string {
	s = strings.ToLower(strings.TrimSpace(s))
	if s == "" {
		return "other"
	}
	for _, c := range Categories {
		if s == c {
			return c
		}
	}
	switch {
	case strings.Contains(s, "keyboard"), strings.Contains(s, "mouse"), strings.Contains(s, "laptop"),
		strings.Contains(s, "computer"), strings.Contains(s, "monitor"), strings.Contains(s, "storage"),
		strings.Contains(s, "usb"), strings.Contains(s, "hub"), strings.Contains(s, "webcam"):
		return "computers"
	case strings.Contains(s, "headphone"), strings.Contains(s, "speaker"), strings.Contains(s, "audio"),
		strings.Contains(s, "earbud"), strings.Contains(s, "microphone"):
		return "audio"
	case strings.Contains(s, "electronic"), strings.Contains(s, "phone"), strings.Contains(s, "camera"),
		strings.Contains(s, "tv"), strings.Contains(s, "charger"):
		return "electronics"
	case strings.Contains(s, "kitchen"), strings.Contains(s, "coffee"), strings.Contains(s, "cook"),
		strings.Contains(s, "kettle"), strings.Contains(s, "blender"):
		return "kitchen"
	case strings.Contains(s, "home"), strings.Contains(s, "furniture"), strings.Contains(s, "lamp"),
		strings.Contains(s, "bed"), strings.Contains(s, "garden"):
		return "home"
	case strings.Contains(s, "office"), strings.Contains(s, "desk"), strings.Contains(s, "chair"),
		strings.Contains(s, "stationery"):
		return "office"
	case strings.Contains(s, "fitness"), strings.Contains(s, "sport"), strings.Contains(s, "gym"),
		strings.Contains(s, "yoga"), strings.Contains(s, "bike"):
		return "fitness"
	case strings.Contains(s, "cloth"), strings.Contains(s, "apparel"), strings.Contains(s, "shoe"),
		strings.Contains(s, "shirt"), strings.Contains(s, "jacket"):
		return "clothing"
	case strings.Contains(s, "book"):
		return "books"
	case strings.Contains(s, "toy"), strings.Contains(s, "game"):
		return "toys"
	}
	return "other"
}
