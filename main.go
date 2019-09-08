package main

import (
	"context"
	"fmt"
	"log"
	"net/http"
	"os"
	"time"

	"cloud.google.com/go/translate"
	"github.com/go-chi/chi"
	"github.com/go-chi/chi/middleware"
	"github.com/gorilla/feeds"
	"github.com/mmcdole/gofeed"
	"github.com/pkg/errors"
	"golang.org/x/oauth2/google"
	"golang.org/x/text/language"
	"google.golang.org/api/option"

	cache "github.com/victorspringer/http-cache"
	"github.com/victorspringer/http-cache/adapter/memory"
)

const cacheSeconds = 3600

var httpClient = &http.Client{
	Timeout: 30 * time.Second,
}

func fetch(url string) (*gofeed.Feed, error) {
	res, err := httpClient.Get(url)
	if err != nil {
		return nil, err
	}
	defer res.Body.Close()

	p := gofeed.NewParser()
	return p.Parse(res.Body)
}

func translateTitle(feed *gofeed.Feed, tag language.Tag) error {
	ctx := context.Background()

	jsonData := os.Getenv("GOOGLE_CLIENT_CREDENTIALS")
	credentials, err := google.CredentialsFromJSON(ctx, ([]byte)(jsonData), translate.Scope)
	if err != nil {
		return errors.WithStack(err)
	}

	client, err := translate.NewClient(ctx, option.WithCredentials(credentials))
	if err != nil {
		return errors.WithStack(err)
	}
	defer client.Close()

	inputs := make([]string, 0, len(feed.Items))
	for _, item := range feed.Items {
		inputs = append(inputs, item.Title)
	}

	translations, err := client.Translate(ctx, inputs, tag, nil)
	if err != nil {
		return errors.WithStack(err)
	}

	for i, item := range feed.Items {
		t := translations[i]
		item.Title = fmt.Sprintf("%s (%s)", t.Text, item.Title)
	}
	return nil
}

func generate(feed *gofeed.Feed) *feeds.Feed {
	items := make([]*feeds.Item, 0, len(feed.Items))
	for _, item := range feed.Items {
		items = append(items, &feeds.Item{
			Title:       item.Title,
			Description: item.Description,
			Link:        &feeds.Link{Href: item.Link},
		})
	}
	return &feeds.Feed{
		Title:       feed.Title,
		Link:        &feeds.Link{Href: feed.Link},
		Description: feed.Description,
		Items:       items,
	}
}

func run() error {
	r := chi.NewRouter()

	r.Use(middleware.Recoverer)

	r.Get("/feed", func(w http.ResponseWriter, r *http.Request) {
		url := r.URL.Query().Get("url")
		if url == "" {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		parsed, err := fetch(url)
		if err != nil {
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		if err := translateTitle(parsed, language.Japanese); err != nil {
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		generated := generate(parsed)
		w.Header().Set("Content-Type", "application/atom+xml")
		w.Header().Set("Cache-Control", "public, max-age="+string(cacheSeconds))
		if err := generated.WriteAtom(w); err != nil {
			w.WriteHeader(http.StatusInternalServerError)
		}
	})

	memcached, err := memory.NewAdapter(
		memory.AdapterWithAlgorithm(memory.LRU),
		memory.AdapterWithCapacity(1024),
	)
	if err != nil {
		return errors.WithStack(err)
	}

	cacheClient, err := cache.NewClient(
		cache.ClientWithAdapter(memcached),
		cache.ClientWithTTL(cacheSeconds*time.Second),
	)
	if err != nil {
		return errors.WithStack(err)
	}

	return http.ListenAndServe("localhost:8080", cacheClient.Middleware(r))
}

func main() {
	if err := run(); err != nil {
		log.Fatal(err)
	}
}
