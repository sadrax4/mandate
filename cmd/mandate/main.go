// Command mandate runs the Mandate server: an AI shopping agent that can only
// spend inside a human-defined mandate and pays through PayPal.
package main

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"syscall"
	"time"

	"mandate/internal/agent"
	"mandate/internal/catalog"
	"mandate/internal/paypal"
	"mandate/internal/server"
	"mandate/internal/store"
)

var version = "dev"

func main() {
	loadDotEnv(".env")
	port := env("PORT", "8080")
	publicURL := env("PUBLIC_URL", "http://localhost:"+port)
	if ext := os.Getenv("RENDER_EXTERNAL_URL"); ext != "" && os.Getenv("PUBLIC_URL") == "" {
		publicURL = ext // Render sets this automatically
	}

	st, err := store.Open(env("DATA_FILE", "data/mandate.json"))
	if err != nil {
		log.Fatalf("open store: %v", err)
	}

	// Catalog: Channel3 when a key is present, otherwise the built-in demo catalog.
	var cat catalog.Searcher
	switch strings.ToLower(env("CATALOG", "")) {
	case "channel3":
		if os.Getenv("CHANNEL3_API_KEY") == "" {
			log.Println("CATALOG=channel3 but CHANNEL3_API_KEY is empty: falling back to the built-in demo catalog")
			cat = catalog.NewMock()
		} else {
			cat = catalog.NewChannel3(os.Getenv("CHANNEL3_API_KEY"), os.Getenv("CHANNEL3_BASE_URL"))
		}
	case "mock", "":
		if os.Getenv("CHANNEL3_API_KEY") != "" && env("CATALOG", "") == "" {
			cat = catalog.NewChannel3(os.Getenv("CHANNEL3_API_KEY"), os.Getenv("CHANNEL3_BASE_URL"))
		} else {
			cat = catalog.NewMock()
		}
	default:
		log.Fatalf("unknown CATALOG %q (mock|channel3)", os.Getenv("CATALOG"))
	}

	// LLM provider. Either an Anthropic Console key (ANTHROPIC_API_KEY) or a
	// bearer token against an Anthropic-compatible gateway such as OpenRouter
	// (ANTHROPIC_AUTH_TOKEN + ANTHROPIC_BASE_URL=https://openrouter.ai/api).
	anthropicOpts := agent.AnthropicOptions{
		APIKey:    os.Getenv("ANTHROPIC_API_KEY"),
		AuthToken: os.Getenv("ANTHROPIC_AUTH_TOKEN"),
		BaseURL:   os.Getenv("ANTHROPIC_BASE_URL"),
		Model:     os.Getenv("ANTHROPIC_MODEL"),
		Effort:    os.Getenv("ANTHROPIC_EFFORT"),
	}
	hasKey := anthropicOpts.APIKey != "" || anthropicOpts.AuthToken != ""
	var provider agent.Provider
	switch strings.ToLower(env("LLM_PROVIDER", "")) {
	case "anthropic":
		if !hasKey {
			log.Fatalf("LLM_PROVIDER=anthropic but neither ANTHROPIC_API_KEY nor ANTHROPIC_AUTH_TOKEN is set")
		}
		provider = agent.NewAnthropic(anthropicOpts)
	case "fake":
		provider = agent.Fake{}
	case "":
		if hasKey {
			provider = agent.NewAnthropic(anthropicOpts)
		} else {
			log.Println("no Anthropic key set: using the scripted offline agent (LLM_PROVIDER=fake)")
			provider = agent.Fake{}
		}
	default:
		log.Fatalf("unknown LLM_PROVIDER %q (anthropic|fake)", os.Getenv("LLM_PROVIDER"))
	}

	// PayPal.
	var pp paypal.Client
	mode := strings.ToLower(env("PAYPAL_MODE", ""))
	if mode == "" {
		if os.Getenv("PAYPAL_CLIENT_ID") != "" {
			mode = "sandbox"
		} else {
			mode = "mock"
		}
	}
	switch mode {
	case "sandbox":
		pp = paypal.NewREST(os.Getenv("PAYPAL_CLIENT_ID"), os.Getenv("PAYPAL_CLIENT_SECRET"), paypal.SandboxBaseURL)
	case "live":
		pp = paypal.NewREST(os.Getenv("PAYPAL_CLIENT_ID"), os.Getenv("PAYPAL_CLIENT_SECRET"), paypal.LiveBaseURL)
	case "mock":
		log.Println("PAYPAL_MODE=mock: using the built-in checkout simulator")
		pp = paypal.NewMock(publicURL)
	default:
		log.Fatalf("unknown PAYPAL_MODE %q (sandbox|live|mock)", mode)
	}

	shopper := agent.NewShopper(provider, cat, st)
	maxRuns, _ := strconv.Atoi(env("MAX_MISSIONS_PER_HOUR", "40"))
	srv := server.New(server.Config{PublicURL: publicURL, StaticDir: env("STATIC_DIR", "web/dist"), Currency: env("CURRENCY", "USD"), Version: version, WebhookID: os.Getenv("PAYPAL_WEBHOOK_ID"), MaxMissionsPerHour: maxRuns}, st, pp, shopper, cat)

	httpSrv := &http.Server{Addr: ":" + port, Handler: srv.Handler(), ReadHeaderTimeout: 10 * time.Second}
	go func() {
		log.Printf("Mandate %s listening on %s (public %s) | llm=%s paypal=%s catalog=%s", version, httpSrv.Addr, publicURL, provider.Name(), pp.Mode(), cat.Name())
		if err := httpSrv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			log.Fatalf("listen: %v", err)
		}
	}()
	stop := make(chan os.Signal, 1)
	signal.Notify(stop, os.Interrupt, syscall.SIGTERM)
	<-stop
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_ = httpSrv.Shutdown(ctx)
	fmt.Println("bye")
}

func env(k, def string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return def
}

// loadDotEnv reads KEY=VALUE lines. Real environment variables win over the
// file; inside the file the last occurrence of a key wins, so editing a value
// at the bottom of .env behaves as people expect.
func loadDotEnv(path string) {
	f, err := os.Open(path)
	if err != nil {
		return
	}
	defer f.Close()
	fromFile := map[string]bool{}
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		line = strings.TrimPrefix(line, "export ")
		k, v, ok := strings.Cut(line, "=")
		if !ok {
			continue
		}
		k, v = strings.TrimSpace(k), strings.TrimSpace(v)
		if i := strings.Index(v, " #"); i >= 0 { // trailing comment
			v = strings.TrimSpace(v[:i])
		}
		v = strings.Trim(v, `"'`)
		if os.Getenv(k) == "" || fromFile[k] {
			os.Setenv(k, v)
			fromFile[k] = true
		}
	}
}
