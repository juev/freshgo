// Package favicon finds, keeps and serves the icons of feeds.
//
// The search follows lib/favicons.php and FreshRSS_Feed::faviconPrepare of
// FreshRSS at commit 219eaf58; where the icons are kept and how they are
// addressed is freshgo's own. See docs/specs/greader-api.md.
package favicon

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	_ "embed"
	"encoding/hex"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/PuerkitoBio/goquery"

	"github.com/juev/freshgo/internal/fetch"
	"github.com/juev/freshgo/internal/store"
)

// Path is where icons are served: Path followed by the hash.
const Path = "/favicon/"

const (
	// recheck is how long an icon is kept before its site is asked again.
	recheck = 14 * 24 * time.Hour
	// maxSize is the largest image accepted as an icon.
	maxSize = 1 << 20
	// Clients may keep an icon for iconMaxAge and the stand-in for a missing
	// one for defaultMaxAge, in seconds.
	iconMaxAge    = 14 * 24 * 3600
	defaultMaxAge = 1800
)

//go:embed default.svg
var defaultIcon []byte

// started stands in for the modification time of the built-in icon.
var started = time.Now()

// Hash is what the icon looked for at source is served by. The salt keeps
// the addresses of the icons from telling which sites the users read.
func Hash(salt, source string) string {
	return digest(salt, "site\x00"+source)
}

// CustomHash is what the icon a user chose for a feed is served by.
func CustomHash(salt string, userID, feedID int64) string {
	return digest(salt, "custom\x00"+strconv.FormatInt(userID, 10)+"\x00"+strconv.FormatInt(feedID, 10))
}

func digest(salt, message string) string {
	mac := hmac.New(sha256.New, []byte(salt))
	mac.Write([]byte(message))
	return hex.EncodeToString(mac.Sum(nil)[:8])
}

// HashOf returns the hash the icon of the feed is served by: of its custom
// icon when it has one, otherwise of the place its icon is looked for.
func HashOf(salt string, f *store.Feed) string {
	if custom(f) {
		return CustomHash(salt, f.UserID, f.ID)
	}
	return Hash(salt, source(f))
}

type iconAttributes struct {
	CustomFavicon bool   `json:"customFavicon"`
	FeedIconURL   string `json:"feedIconUrl"`
}

func attributes(f *store.Feed) iconAttributes {
	var a iconAttributes
	// Attributes of another shape mean no settings for the icon.
	_ = json.Unmarshal(f.Attributes, &a)
	return a
}

func custom(f *store.Feed) bool {
	return attributes(f).CustomFavicon
}

// source is where the icon of a feed is looked for: the image the feed
// names, else its site, else the root of the host the feed lives on.
func source(f *store.Feed) string {
	if icon := attributes(f).FeedIconURL; icon != "" {
		return icon
	}
	if f.Website != "" && f.Website != f.URL {
		return f.Website
	}
	if root := rootOf(f.URL); root != "" {
		return root
	}
	return f.URL
}

// rootOf returns scheme://host/ of an http(s) address, or "".
func rootOf(raw string) string {
	u, err := url.Parse(raw)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return ""
	}
	return u.Scheme + "://" + u.Host + "/"
}

// Service fetches the icons of feeds into the database and serves them.
type Service struct {
	db     *store.Store
	client *fetch.Client
	log    *slog.Logger
	now    func() time.Time
}

// New returns a Service.
func New(db *store.Store, client *fetch.Client, log *slog.Logger) *Service {
	return &Service{db: db, client: client, log: log, now: time.Now}
}

// Refresh makes sure the icon of the feed is there and not older than two
// weeks, asking the site when it is not. A failure is logged: the feed does
// without an icon until the next attempt.
func (s *Service) Refresh(ctx context.Context, f *store.Feed) {
	if custom(f) {
		return
	}
	if err := s.refresh(ctx, source(f)); err != nil && ctx.Err() == nil {
		s.log.Warn("icon is not stored", "user_id", f.UserID, "feed", f.ID, "error", err)
	}
}

func (s *Service) refresh(ctx context.Context, src string) error {
	salt, err := s.db.Salt(ctx)
	if err != nil {
		return err
	}
	now := s.now()
	icon, err := s.db.Icon(ctx, Hash(salt, src))
	switch {
	case errors.Is(err, store.ErrNotFound):
		icon = &store.Icon{Hash: Hash(salt, src), Source: src}
	case err != nil:
		return err
	case now.Sub(time.Unix(icon.Checked, 0)) < recheck:
		return nil
	}
	icon.Checked = now.Unix()
	// A site that has stopped answering keeps the icon it had.
	if content, contentType := s.find(ctx, src); content != nil && !bytes.Equal(content, icon.Content) {
		icon.Content, icon.ContentType, icon.Modified = content, contentType, now.Unix()
	}
	return s.db.PutIcon(ctx, icon)
}

// find returns the icon found starting at src and its media type, or nil.
// src is taken as an image first, then as a page that names its icon, then
// the same is tried at the root of the site, and last comes /favicon.ico.
func (s *Service) find(ctx context.Context, src string) ([]byte, string) {
	if content, contentType := s.fromPage(ctx, src); content != nil {
		return content, contentType
	}
	root := rootOf(src)
	if root == "" {
		return nil, ""
	}
	if root != src {
		if content, contentType := s.fromPage(ctx, root); content != nil {
			return content, contentType
		}
	}
	return s.image(ctx, root+"favicon.ico")
}

// fromPage fetches the address and returns it when it is an image, else the
// first image among the icons the page links to.
func (s *Service) fromPage(ctx context.Context, address string) ([]byte, string) {
	resp, err := s.client.Fetch(ctx, fetch.Request{URL: address, Accept: fetch.AcceptHTML})
	if err != nil {
		return nil, ""
	}
	if contentType := ImageType(resp.Body); contentType != "" {
		return resp.Body, contentType
	}
	doc, err := goquery.NewDocumentFromReader(bytes.NewReader(resp.Body))
	if err != nil {
		return nil, ""
	}
	base, err := url.Parse(resp.URL)
	if err != nil {
		return nil, ""
	}
	if href, ok := doc.Find("base[href]").First().Attr("href"); ok {
		if u, err := base.Parse(strings.TrimSpace(href)); err == nil {
			base = u
		}
	}
	var (
		content     []byte
		contentType string
	)
	doc.Find("link[href]").EachWithBreak(func(_ int, link *goquery.Selection) bool {
		rel := strings.ToLower(link.AttrOr("rel", ""))
		if rel != "icon" && rel != "shortcut icon" {
			return true
		}
		u, err := base.Parse(strings.TrimSpace(link.AttrOr("href", "")))
		if err != nil {
			return true
		}
		content, contentType = s.image(ctx, u.String())
		return content == nil
	})
	return content, contentType
}

// image fetches the address and returns the body when it is an image.
func (s *Service) image(ctx context.Context, address string) ([]byte, string) {
	resp, err := s.client.Fetch(ctx, fetch.Request{URL: address, Accept: fetch.AcceptIcon})
	if err != nil {
		return nil, ""
	}
	if contentType := ImageType(resp.Body); contentType != "" {
		return resp.Body, contentType
	}
	return nil, ""
}

// ImageType returns the media type of an image that can serve as an icon,
// or "" for anything else.
func ImageType(body []byte) string {
	if len(body) == 0 || len(body) > maxSize {
		return ""
	}
	sniffed := http.DetectContentType(body)
	if strings.HasPrefix(sniffed, "image/") {
		return sniffed
	}
	// SVG is text as far as sniffing goes.
	head := body[:min(len(body), 1024)]
	if strings.HasPrefix(sniffed, "text/") && bytes.Contains(head, []byte("<svg")) {
		return "image/svg+xml"
	}
	return ""
}

// ServeHTTP answers Path<hash> with the icon: the user's own, the one found
// at the site, or the built-in one when there is neither.
func (s *Service) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		w.Header().Set("Allow", "GET, HEAD")
		http.Error(w, "Method Not Allowed", http.StatusMethodNotAllowed)
		return
	}
	hash := strings.TrimPrefix(r.URL.Path, Path)
	content, contentType, modified, err := s.lookup(r.Context(), hash)
	if err != nil {
		s.log.Error("icon is not served", "hash", hash, "error", err)
		http.Error(w, "Internal Server Error", http.StatusInternalServerError)
		return
	}
	maxAge := iconMaxAge
	if content == nil {
		content, contentType, modified, maxAge = defaultIcon, "image/svg+xml", started, defaultMaxAge
	}
	// An icon is somebody else's file: it must not run as a document of ours.
	h := w.Header()
	h.Set("Content-Security-Policy", "default-src 'none'; frame-ancestors 'none'; sandbox")
	h.Set("X-Content-Type-Options", "nosniff")
	h.Set("Content-Type", contentType)
	h.Set("Cache-Control", "max-age="+strconv.Itoa(maxAge))
	http.ServeContent(w, r, "", modified, bytes.NewReader(content))
}

// lookup returns the stored icon for a hash; content is nil when there is none.
func (s *Service) lookup(ctx context.Context, hash string) (content []byte, contentType string, modified time.Time, err error) {
	if own, err := s.db.CustomIconByHash(ctx, hash); err == nil {
		return own.Content, typeOr(own.Content), time.Unix(own.Modified, 0), nil
	} else if !errors.Is(err, store.ErrNotFound) {
		return nil, "", time.Time{}, err
	}
	icon, err := s.db.Icon(ctx, hash)
	if errors.Is(err, store.ErrNotFound) || err == nil && icon.Content == nil {
		return nil, "", time.Time{}, nil
	}
	if err != nil {
		return nil, "", time.Time{}, err
	}
	return icon.Content, icon.ContentType, time.Unix(icon.Modified, 0), nil
}

// typeOr is the media type of an icon the user supplied: what it looks
// like, or the type of .ico files when it looks like nothing known.
func typeOr(content []byte) string {
	if contentType := ImageType(content); contentType != "" {
		return contentType
	}
	return "image/x-icon"
}
