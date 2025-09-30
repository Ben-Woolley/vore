package favicon

import (
	"context"
	"encoding/base64"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"sync"
	"time"

	"golang.org/x/net/html"
)

type FaviconCache struct {
	// domain names -> b64 data URLs
	cache map[string]string
	mutex sync.RWMutex
}

type FaviconFetcher struct {
	cache  *FaviconCache
	client *http.Client
}

func NewFaviconFetcher() *FaviconFetcher {
	return &FaviconFetcher{
		cache: &FaviconCache{
			cache: make(map[string]string),
		},
		client: &http.Client{
			Timeout: 10 * time.Second,
		},
	}
}

func (f *FaviconFetcher) GetFaviconDataURL(domain string) string {
	f.cache.mutex.RLock()
	defer f.cache.mutex.RUnlock()

	return f.cache.cache[domain]
}

func (f *FaviconFetcher) FetchFaviconsForDomains(feedURLs []string) {
	domains := f.extractUniqueDomains(feedURLs)

	log.Printf("favicon: starting to fetch favicons for %d unique domains", len(domains))

	const maxWorkers = 5
	domainChan := make(chan string, len(domains))
	var wg sync.WaitGroup

	for range maxWorkers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for domain := range domainChan {
				f.fetchFaviconForDomain(domain)
			}
		}()
	}

	for _, domain := range domains {
		domainChan <- domain
	}
	close(domainChan)

	wg.Wait()

	log.Printf("favicon: finished fetching favicons, cached %d successful results", len(f.cache.cache))
}

// extractUniqueDomains extracts unique domain names from a list of URLs
func (f *FaviconFetcher) extractUniqueDomains(urls []string) []string {
	// use a map as a cheap "uniqueness" filter okie dokie
	domainSet := make(map[string]bool)

	for _, rawURL := range urls {
		parsedURL, err := url.Parse(rawURL)
		if err != nil {
			continue
		}

		domain := parsedURL.Hostname()
		if domain != "" {
			domainSet[domain] = true
		}
	}

	domains := make([]string, 0, len(domainSet))
	for domain := range domainSet {
		domains = append(domains, domain)
	}

	return domains
}

// fetchFaviconForDomain attempts to fetch a favicon for a specific domain
// It first tries to parse the HTML page to find favicon link tags, then falls back to common paths
func (f *FaviconFetcher) fetchFaviconForDomain(domain string) {
	baseURL := fmt.Sprintf("https://%s", domain)

	faviconURLs := f.discoverFaviconFromHTML(baseURL)
	faviconURLs = append(faviconURLs,
		fmt.Sprintf("https://%s/favicon.ico", domain),
		fmt.Sprintf("https://%s/favicon.png", domain),
		fmt.Sprintf("https://%s/apple-touch-icon.png", domain),
		fmt.Sprintf("https://%s/apple-touch-icon-precomposed.png", domain),
	)

	for _, faviconURL := range faviconURLs {
		dataURL := f.fetchFaviconDataURL(faviconURL)
		if dataURL != "" {
			f.cache.mutex.Lock()
			f.cache.cache[domain] = dataURL
			f.cache.mutex.Unlock()
			return
		}
	}

	log.Printf("favicon: no favicon found for domain %s", domain)
}

// fetchFaviconDataURL fetches a favicon from the given URL and converts it to a data URL
// Returns empty string if the favicon could not be fetched or converted
func (f *FaviconFetcher) fetchFaviconDataURL(faviconURL string) string {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, faviconURL, nil)
	if err != nil {
		return ""
	}

	req.Header.Set("User-Agent", "vore: favicon fetcher")

	resp, err := f.client.Do(req)
	if err != nil {
		return ""
	}
	defer resp.Body.Close()

	// 2xx == good
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return ""
	}

	// make sure it looks like an img
	contentType := resp.Header.Get("Content-Type")
	if contentType != "" && !strings.HasPrefix(contentType, "image/") &&
		!strings.Contains(contentType, "icon") && !strings.Contains(contentType, "octet-stream") {
		return ""
	}

	const maxFaviconSize = 1024 * 1024 // 1MB limit
	data, err := io.ReadAll(io.LimitReader(resp.Body, maxFaviconSize))
	if err != nil {
		return ""
	}

	if len(data) == 0 {
		return ""
	}

	if contentType == "" {
		if strings.HasSuffix(faviconURL, ".png") {
			contentType = "image/png"
		} else if strings.HasSuffix(faviconURL, ".ico") {
			contentType = "image/x-icon"
		} else {
			contentType = "image/x-icon" // fallback
		}
	}

	// data URL == base64 encoded favicon
	encodedData := base64.StdEncoding.EncodeToString(data)
	dataURL := fmt.Sprintf("data:%s;base64,%s", contentType, encodedData)

	return dataURL
}

// discoverFaviconFromHTML fetches the HTML page and parses it to find favicon link tags
func (f *FaviconFetcher) discoverFaviconFromHTML(baseURL string) []string {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, baseURL, nil)
	if err != nil {
		return []string{}
	}

	req.Header.Set("User-Agent", "vore: favicon fetcher")

	resp, err := f.client.Do(req)
	if err != nil {
		return []string{}
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return []string{}
	}

	doc, err := html.Parse(resp.Body)
	if err != nil {
		return []string{}
	}

	parsedBaseURL, err := url.Parse(baseURL)
	if err != nil {
		return []string{}
	}

	return f.extractFaviconURLsFromHTML(doc, parsedBaseURL)
}

// extractFaviconURLsFromHTML walks the HTML document tree and extracts favicon URLs
func (f *FaviconFetcher) extractFaviconURLsFromHTML(doc *html.Node, baseURL *url.URL) []string {
	var faviconURLs []string
	var walkHTML func(*html.Node)

	walkHTML = func(n *html.Node) {
		if n.Type == html.ElementNode && n.Data == "link" {
			var rel, href, sizes string
			for _, attr := range n.Attr {
				switch attr.Key {
				case "rel":
					rel = strings.ToLower(attr.Val)
				case "href":
					href = attr.Val
				case "sizes":
					sizes = attr.Val
				}
			}

			// check if this is a favicon-related link
			if f.isFaviconRel(rel) && href != "" {
				// make href absolute if necessary
				faviconURL, err := baseURL.Parse(href)
				if err == nil {
					// prioritize larger icons by putting them first
					if f.isLargerIcon(sizes) {
						faviconURLs = append([]string{faviconURL.String()}, faviconURLs...)
					} else {
						faviconURLs = append(faviconURLs, faviconURL.String())
					}
				}
			}
		}

		// recursively walk child nodes
		for c := n.FirstChild; c != nil; c = c.NextSibling {
			walkHTML(c)
		}
	}

	walkHTML(doc)
	return faviconURLs
}

// isFaviconRel checks if a link rel attribute indicates a favicon
func (f *FaviconFetcher) isFaviconRel(rel string) bool {
	faviconRels := []string{
		"icon",
		"shortcut icon",
		"apple-touch-icon",
		"apple-touch-icon-precomposed",
		"mask-icon",
	}

	if slices.Contains(faviconRels, rel) {
		return true
	}
	return false
}

// isLargerIcon checks if the sizes attribute indicates a larger icon (for prioritization)
func (f *FaviconFetcher) isLargerIcon(sizes string) bool {
	return strings.Contains(sizes, "32x32") ||
		strings.Contains(sizes, "64x64") ||
		strings.Contains(sizes, "128x128") ||
		strings.Contains(sizes, "192x192")
}
