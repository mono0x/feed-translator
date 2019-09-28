package main

import (
	"context"
	"fmt"
	"log"
	"net/http"
	"os"
	"sort"
	"time"

	"cloud.google.com/go/translate"
	"github.com/go-chi/chi"
	"github.com/go-chi/chi/middleware"
	"github.com/gorilla/feeds"
	"github.com/mmcdole/gofeed"
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
		return fmt.Errorf("%w", err)
	}

	client, err := translate.NewClient(ctx, option.WithCredentials(credentials))
	if err != nil {
		return fmt.Errorf("%w", err)
	}
	defer client.Close()

	inputs := make([]string, 0, len(feed.Items))
	for _, item := range feed.Items {
		inputs = append(inputs, item.Title)
	}

	translations, err := client.Translate(ctx, inputs, tag, nil)
	if err != nil {
		return fmt.Errorf("%w", err)
	}

	for i, item := range feed.Items {
		t := translations[i]
		item.Title = fmt.Sprintf("%s (%s)", t.Text, item.Title)
	}
	return nil
}

func filter(items []*gofeed.Item) []*gofeed.Item {
	sort.SliceStable(items, func(i, j int) bool {
		var l, r time.Time
		if items[i].UpdatedParsed != nil {
			l = *items[i].UpdatedParsed
		} else if items[i].PublishedParsed != nil {
			l = *items[i].PublishedParsed
		}
		if items[j].UpdatedParsed != nil {
			r = *items[j].UpdatedParsed
		} else if items[j].PublishedParsed != nil {
			r = *items[j].PublishedParsed
		}
		return l.After(r)
	})
	if len(items) > 10 {
		return items[0:10]
	} else {
		return items
	}
}

func generate(feed *gofeed.Feed) *feeds.Feed {
	filtered := filter(feed.Items)
	items := make([]*feeds.Item, 0, len(filtered))
	for _, item := range filtered {
		var author *feeds.Author
		if item.Author != nil {
			author = &feeds.Author{Name: item.Author.Name, Email: item.Author.Email}
		}
		var created time.Time
		if item.PublishedParsed != nil {
			created = *item.PublishedParsed
		}
		var updated time.Time
		if item.UpdatedParsed != nil {
			updated = *item.UpdatedParsed
		}
		items = append(items, &feeds.Item{
			Title:       item.Title,
			Description: item.Description,
			Link:        &feeds.Link{Href: item.Link},
			Author:      author,
			Created:     created,
			Updated:     updated,
		})
	}
	var author *feeds.Author
	if feed.Author != nil {
		author = &feeds.Author{Name: feed.Author.Name, Email: feed.Author.Email}
	}
	var created time.Time
	if feed.PublishedParsed != nil {
		created = *feed.PublishedParsed
	}
	var updated time.Time
	if feed.UpdatedParsed != nil {
		updated = *feed.UpdatedParsed
	}
	return &feeds.Feed{
		Title:       feed.Title,
		Link:        &feeds.Link{Href: feed.Link},
		Description: feed.Description,
		Items:       items,
		Author:      author,
		Created:     created,
		Updated:     updated,
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
			log.Println(err)
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		if err := translateTitle(parsed, language.Japanese); err != nil {
			log.Println(err)
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		generated := generate(parsed)
		w.Header().Set("Content-Type", "application/atom+xml")
		w.Header().Set("Cache-Control", "public, max-age="+string(cacheSeconds))
		if err := generated.WriteAtom(w); err != nil {
			log.Println(err)
			w.WriteHeader(http.StatusInternalServerError)
		}
	})

	memcached, err := memory.NewAdapter(
		memory.AdapterWithAlgorithm(memory.LRU),
		memory.AdapterWithCapacity(1024),
	)
	if err != nil {
		return fmt.Errorf("%w", err)
	}

	cacheClient, err := cache.NewClient(
		cache.ClientWithAdapter(memcached),
		cache.ClientWithTTL(cacheSeconds*time.Second),
	)
	if err != nil {
		return fmt.Errorf("%w", err)
	}

	return http.ListenAndServe("localhost:8080", cacheClient.Middleware(r))
}

func main() {
	if err := run(); err != nil {
		log.Fatal(err)
	}
}
