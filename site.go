package main

import (
	"context"
	"errors"
	"fmt"
	"html/template"
	"log"
	"math/rand"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	"git.j3s.sh/vore/lib"
	"git.j3s.sh/vore/reaper"
	"git.j3s.sh/vore/rss"
	"git.j3s.sh/vore/sqlite"
	"git.j3s.sh/vore/wayback"
	"golang.org/x/crypto/bcrypt"
	"golang.org/x/net/html"
)

type Site struct {
	// title of the website
	title string

	// contains every single feed
	reaper *reaper.Reaper

	// site database handle
	db *sqlite.DB
}

type Save struct {
	// inferred: user_id
}

// New returns a fully populated & ready for action Site
func New() *Site {
	// pragmas:
	// - journal_mode=WAL: enable write-ahead log for concurrency & perf
	// - foreign_keys=ON: need foreign keyz
	// - busy_timeout=5000: locky locky 5 secs
	// - synchronous=NORMAL: "The synchronous=NORMAL setting is a good choice for most applications running in WAL mode."
	// - cache_size=-64000: 64MB ram for db cache (yum yum more perf)
	err := os.MkdirAll("data", 0700)
	if err != nil {
		panic(err)
	}
	db := sqlite.New("data/vore.db?_pragma=journal_mode(WAL)&_pragma=foreign_keys(ON)&_pragma=busy_timeout(5000)&_pragma=synchronous(NORMAL)&_pragma=cache_size(-64000)")
	s := Site{
		title:  "vore",
		reaper: reaper.New(db),
		db:     db,
	}
	return &s
}

func (s *Site) staticHandler(w http.ResponseWriter, r *http.Request) {
	file := filepath.Join("files", "static", r.PathValue("file"))
	if _, err := os.Stat(file); !errors.Is(err, os.ErrNotExist) {
		http.ServeFile(w, r, file)
		return
	}
	http.NotFound(w, r)
}

func (s *Site) indexHandler(w http.ResponseWriter, r *http.Request) {
	if s.loggedIn(r) {
		username := s.username(r)
		http.Redirect(w, r, "/"+username, http.StatusSeeOther)
		return
	}
	s.renderPage(w, r, "index", nil)
}

func (s *Site) discoverHandler(w http.ResponseWriter, r *http.Request) {
	s.renderPage(w, r, "discover", nil)
}

func (s *Site) loginHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method == "GET" {
		if s.loggedIn(r) {
			username := s.username(r)
			http.Redirect(w, r, "/"+username, http.StatusSeeOther)
		} else {
			s.renderPage(w, r, "login", nil)
		}
	}
	if r.Method == "POST" {
		username := r.FormValue("username")
		password := r.FormValue("password")

		err := s.login(w, username, password)
		if err != nil {
			s.renderErr(w, err.Error(), http.StatusUnauthorized)
			return
		}
		http.Redirect(w, r, "/"+username, http.StatusSeeOther)
	}
}

// TODO: make this take a POST only in accordance w/ some spec
func (s *Site) logoutHandler(w http.ResponseWriter, r *http.Request) {
	http.SetCookie(w, &http.Cookie{
		Name:  "session_token",
		Value: "",
	})
	http.Redirect(w, r, "/", http.StatusSeeOther)
}

func (s *Site) registerHandler(w http.ResponseWriter, r *http.Request) {
	username := r.FormValue("username")
	password := r.FormValue("password")
	err := s.register(username, password)
	if err != nil {
		s.renderErr(w, err.Error(), http.StatusInternalServerError)
		return
	}
	err = s.login(w, username, password)
	if err != nil {
		s.renderErr(w, err.Error(), http.StatusInternalServerError)
		return
	}
	http.Redirect(w, r, "/", http.StatusSeeOther)
}

// saveHandler is an endpoint that takes a url, archives it
// via archive.org, and then saves it to the user's account.
func (s *Site) saveHandler(w http.ResponseWriter, r *http.Request) {
	if !s.loggedIn(r) {
		s.renderErr(w, "", http.StatusUnauthorized)
		return
	}

	username := s.username(r)
	encodedURL := r.PathValue("url")
	decodedURL, err := url.QueryUnescape(encodedURL)
	if err != nil {
		e := fmt.Sprintf("failed to decode URL '%s' %s", encodedURL, err)
		s.renderErr(w, e, http.StatusBadRequest)
		return
	}

	item, err := s.reaper.GetItem(decodedURL)
	if err != nil {
		fmt.Fprintf(w, "error!")
		return
	}

	c := wayback.Client{}

	archiveURL, err := c.Archive(context.Background(), decodedURL)
	if err != nil {
		log.Println(err)
		fmt.Fprintf(w, "error capturing archive!!")
		return
	}

	err = s.db.WriteSavedItem(username, sqlite.SavedItem{
		ArchiveURL: archiveURL,
		ItemTitle:  item.Title,
		ItemURL:    item.Link,
	})
	if err != nil {
		log.Println(err)
		fmt.Fprintf(w, "error!!!")
		return
	}
}

func (s *Site) userHandler(w http.ResponseWriter, r *http.Request) {
	username := r.PathValue("username")

	if !s.db.UserExists(username) {
		http.NotFound(w, r)
		return
	}

	items := s.reaper.TrimFuturePosts(s.reaper.SortFeedItemsByDate(s.reaper.GetUserFeeds(username)))
	data := struct {
		User  string
		Items []*rss.Item
	}{
		User:  username,
		Items: items,
	}

	s.renderPage(w, r, "user", data)
}

func (s *Site) userSavesHandler(w http.ResponseWriter, r *http.Request) {
	if !s.loggedIn(r) {
		s.renderErr(w, "", http.StatusUnauthorized)
		return
	}

	username := s.username(r)
	saves := s.db.GetUserSavedItems(username)
	s.renderPage(w, r, "saves", saves)
}

func (s *Site) settingsHandler(w http.ResponseWriter, r *http.Request) {
	if !s.loggedIn(r) {
		s.renderErr(w, "", http.StatusUnauthorized)
		return
	}

	var feeds []*rss.Feed
	feeds = s.reaper.GetUserFeeds(s.username(r))
	s.renderPage(w, r, "settings", feeds)
}

// TODO: show diff before submission (like tf plan)
// check if feed exists in db already?
// validate that title exists
func (s *Site) settingsSubmitHandler(w http.ResponseWriter, r *http.Request) {
	if !s.loggedIn(r) {
		s.renderErr(w, "", http.StatusUnauthorized)
		return
	}

	// validate user input
	var validatedURLs []string
	for _, inputURL := range strings.Split(r.FormValue("submit"), "\r\n") {
		inputURL = strings.TrimSpace(inputURL)
		if inputURL == "" {
			continue
		}

		// if the entry is already in reaper, don't validate
		if s.reaper.HasFeed(inputURL) {
			validatedURLs = append(validatedURLs, inputURL)
			continue
		}
		if _, err := url.ParseRequestURI(inputURL); err != nil {
			e := fmt.Sprintf("can't parse url '%s': %s", inputURL, err)
			s.renderErr(w, e, http.StatusBadRequest)
			return
		}
		validatedURLs = append(validatedURLs, inputURL)
	}

	// write to reaper + db
	for _, u := range validatedURLs {
		// if it's in reaper, it's in the db, safe to skip
		if s.reaper.HasFeed(u) {
			continue
		}
		err := s.reaper.Fetch(u)
		if err != nil {
			e := fmt.Sprintf("reaper: can't fetch '%s' %s", u, err)
			s.renderErr(w, e, http.StatusBadRequest)
			return
		}
		s.db.WriteFeed(u)
	}

	err := s.db.BatchSubscribe(s.username(r), validatedURLs)
	if err != nil {
		log.Println(err)
		e := fmt.Sprintf("reaper: can't batchsubscribe user=%s err=%s", s.username(r), err)
		s.renderErr(w, e, http.StatusInternalServerError)
		return
	}

	http.Redirect(w, r, "/settings", http.StatusSeeOther)
}

func (s *Site) feedDetailsHandler(w http.ResponseWriter, r *http.Request) {
	encodedURL := r.PathValue("url")
	decodedURL, err := url.QueryUnescape(encodedURL)
	if err != nil {
		e := fmt.Sprintf("failed to decode URL '%s' %s", encodedURL, err)
		s.renderErr(w, e, http.StatusBadRequest)
		return
	}
	fetchErr, err := s.db.GetFeedFetchError(decodedURL)
	if err != nil {
		e := fmt.Sprintf("failed to fetch feed error '%s' %s", encodedURL, err)
		s.renderErr(w, e, http.StatusBadRequest)
		return
	}

	feedData := struct {
		Feed         *rss.Feed
		FetchFailure string
	}{
		Feed:         s.reaper.GetFeed(decodedURL),
		FetchFailure: fetchErr,
	}

	s.renderPage(w, r, "feedDetails", feedData)
}

func (s *Site) fingerHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method == "GET" {
		s.renderPage(w, r, "finger", nil)
	}
	if r.Method == "POST" {
		targetURL := r.FormValue("url")
		if targetURL == "" {
			http.Error(w, "Please provide a URL.", http.StatusBadRequest)
			return
		}

		parsed, err := url.ParseRequestURI(targetURL)
		if err != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") {
			http.Error(w, "Invalid URL (only http/https allowed).", http.StatusBadRequest)
			return
		}

		ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
		defer cancel()

		req, err := http.NewRequestWithContext(ctx, http.MethodGet, targetURL, nil)
		if err != nil {
			http.Error(w, "failed to build request: "+err.Error(), http.StatusInternalServerError)
			return
		}

		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			http.Error(w, "failed to fetch URL: "+err.Error(), http.StatusBadGateway)
			return
		}
		defer resp.Body.Close()

		if resp.StatusCode < 200 || resp.StatusCode > 299 {
			http.Error(w, "non-2xx status from site: "+resp.Status, http.StatusBadGateway)
			return
		}

		doc, err := html.Parse(resp.Body)
		if err != nil {
			http.Error(w, "failed to parse HTML: "+err.Error(), http.StatusInternalServerError)
			return
		}

		feeds := discoverFeeds(doc, parsed)

		// Display the results
		fmt.Fprintf(w, `<!DOCTYPE html>
<html>
<head>
    <meta charset="utf-8">
    <title>%s feeds</title>
</head>
<body style="font-family:sans-serif;">`, html.EscapeString(targetURL))

		if len(feeds) == 0 {
			fmt.Fprintln(w, `<p><em>No RSS/Atom feeds found</em></p>`)
		} else {
			fmt.Fprintln(w, `<ul>`)
			for _, f := range feeds {
				fmt.Fprintf(w, `<li>%s</li>`, html.EscapeString(f))
			}
			fmt.Fprintln(w, `</ul>`)
		}
		fmt.Fprintln(w, `</body></html>`)
	}
}

func discoverFeeds(doc *html.Node, base *url.URL) []string {
	var feeds []string
	var f func(*html.Node)
	f = func(n *html.Node) {
		if n.Type == html.ElementNode && n.Data == "link" {
			var rel, typ, href string
			for _, attr := range n.Attr {
				switch attr.Key {
				case "rel":
					rel = attr.Val
				case "type":
					typ = attr.Val
				case "href":
					href = attr.Val
				}
			}

			if rel == "alternate" && (typ == "application/rss+xml" || typ == "application/atom+xml") {
				// make href absolute if necessary
				u, err := base.Parse(href)
				if err == nil {
					feeds = append(feeds, u.String())
				}
			}
		}
		for c := n.FirstChild; c != nil; c = c.NextSibling {
			f(c)
		}
	}
	f(doc)
	return feeds
}

// username fetches a client's username based
// on the sessionToken that user has set. username
// will return "" if there is no sessionToken.
func (s *Site) username(r *http.Request) string {
	cookie, err := r.Cookie("session_token")
	if err == http.ErrNoCookie {
		return ""
	}
	if err != nil {
		log.Println(err)
	}
	username := s.db.GetUsernameBySessionToken(cookie.Value)
	return username
}

func (s *Site) loggedIn(r *http.Request) bool {
	if s.username(r) == "" {
		return false
	}
	return true
}

// login compares the sqlite password field against the user supplied password and
// sets a session token against the supplied writer.
func (s *Site) login(w http.ResponseWriter, username string, password string) error {
	if username == "" {
		return fmt.Errorf("username cannot be empty")
	}
	if password == "" {
		return fmt.Errorf("password cannot be empty")
	}
	if !s.db.UserExists(username) {
		return fmt.Errorf("user '%s' does not exist", username)
	}
	storedPassword := s.db.GetPassword(username)
	err := bcrypt.CompareHashAndPassword([]byte(storedPassword), []byte(password))
	if err != nil {
		return fmt.Errorf("invalid password")
	}
	sessionToken, err := s.db.GetSessionToken(username)
	if err != nil {
		return err
	}
	if sessionToken == "" {
		sessionToken = lib.GenerateSecureToken(32)
		err := s.db.SetSessionToken(username, sessionToken)
		if err != nil {
			return err
		}
	}
	http.SetCookie(w, &http.Cookie{
		Name:    "session_token",
		Expires: time.Now().Add(time.Hour * 24 * 365),
		Value:   sessionToken,
	})
	return nil
}

func (s *Site) register(username string, password string) error {
	if s.db.UserExists(username) {
		return fmt.Errorf("user '%s' already exists", username)
	}
	hashedPassword, err := bcrypt.GenerateFromPassword([]byte(password), bcrypt.DefaultCost)
	if err != nil {
		return err
	}

	err = s.db.AddUser(username, string(hashedPassword))
	if err != nil {
		return err
	}
	return nil
}

// renderPage renders the given page and passes data to the
// template execution engine. it's normally the last thing a
// handler should do tbh.
func (s *Site) renderPage(w http.ResponseWriter, r *http.Request, page string, data any) {
	funcMap := template.FuncMap{
		"printDomain": s.printDomain,
		"timeSince":   s.timeSince,
		"trimSpace":   strings.TrimSpace,
		"escapeURL":   url.QueryEscape,
	}

	tmplFiles := filepath.Join("files", "*.tmpl.html")
	tmpl := template.Must(template.New("whatever").Funcs(funcMap).ParseGlob(tmplFiles))

	// fields on this anon struct are generally
	// pulled out of Data when they're globally required
	// callers should jam anything they want into Data
	pageData := struct {
		Title      string
		Username   string
		LoggedIn   bool
		CutePhrase string
		Data       any
	}{
		Title:      page,
		Username:   s.username(r),
		LoggedIn:   s.loggedIn(r),
		CutePhrase: s.randomCutePhrase(),
		Data:       data,
	}

	err := tmpl.ExecuteTemplate(w, page, pageData)
	if err != nil {
		s.renderErr(w, err.Error(), http.StatusInternalServerError)
		return
	}
}

// printDomain does a best-effort uri parse, returning a string
// that may still contain special characters
func (s *Site) printDomain(rawURL string) string {
	parsedURL, err := url.Parse(rawURL)
	if err == nil {
		return parsedURL.Hostname()
	}
	// do our best to trim it manually if url parsing fails
	trimmedStr := strings.TrimSpace(rawURL)
	trimmedStr = strings.TrimPrefix(trimmedStr, "http://")
	trimmedStr = strings.TrimPrefix(trimmedStr, "https://")

	return strings.Split(trimmedStr, "/")[0]
}

func (s *Site) timeSince(t time.Time) string {
	now := time.Now()
	duration := now.Sub(t)

	minutes := int(duration.Minutes())
	hours := int(duration.Hours())
	days := int(duration.Hours() / 24)
	weeks := int(duration.Hours() / (24 * 7))
	months := int(duration.Hours() / (24 * 7 * 4))
	years := int(duration.Hours() / (24 * 7 * 4 * 12))

	if years > 100 {
		return fmt.Sprintf("over 100 years ago ಠ_ಠ")
	} else if years > 1 {
		return fmt.Sprintf("%d years ago", years)
	} else if months > 1 {
		return fmt.Sprintf("%d months ago", months)
	} else if weeks > 1 {
		return fmt.Sprintf("%d weeks ago", weeks)
	} else if days > 1 {
		return fmt.Sprintf("%d days ago", days)
	} else if hours > 1 {
		return fmt.Sprintf("%d hours ago", hours)
	} else if minutes > 1 {
		return fmt.Sprintf("%d mins ago", minutes)
	} else {
		return fmt.Sprintf("just now")
	}
}

// renderErr sets the correct http status in the header,
// optionally decorates certain errors, then renders the err page
func (s *Site) renderErr(w http.ResponseWriter, error string, code int) {
	var prefix string
	switch code {
	case http.StatusBadRequest:
		prefix = "400 bad request\n"
	case http.StatusUnauthorized:
		prefix = "401 unauthorized\n"
	case http.StatusInternalServerError:
		prefix = "(╥﹏╥) oopsie woopsie, uwu\n"
		prefix += "we made a fucky wucky (╥﹏╥)\n\n"
		prefix += "500 internal server error\n"
	}
	log.Println(prefix + error)
	http.Error(w, prefix+error, code)
}

func (s *Site) randomCutePhrase() string {
	phrases := []string{
		"nom nom posts (๑ᵔ⤙ᵔ๑)",
		"^(;,;)^ vawr",
		"( -_•)╦̵̵̿╤─ - - - vore",
		"devouring feeds since 2023",
		"tfw new rss post (⊙ _ ⊙ )",
		"( ˘͈ ᵕ ˘͈♡) <3",
		"voreposting",
		"vore dot website",
		"a no-bullshit feed reader",
		"*chomp* good feeds",
	}
	i := rand.Intn(len(phrases))
	return phrases[i]
}
