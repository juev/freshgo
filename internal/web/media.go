package web

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"mime"
	"net/http"
	"strconv"
	"strings"

	"github.com/juev/freshgo/internal/fetch"
	"github.com/juev/freshgo/internal/mediaproxy"
)

const (
	// acceptImage is what a site is asked for when an image is fetched for
	// a reader.
	acceptImage = "image/avif,image/webp,image/png,image/svg+xml,image/*;q=0.8,*/*;q=0.1"
	// mediaCache tells the browser to keep an image for three days.
	mediaCache = "private, max-age=259200"
	// mediaSecurityPolicy keeps an image that is opened on its own, an SVG
	// with a script in it included, from doing anything.
	mediaSecurityPolicy = "default-src 'none'; style-src 'unsafe-inline'; sandbox"
)

// ImageFetcher fetches the images the server hands out from its own address.
type ImageFetcher interface {
	Fetch(ctx context.Context, req fetch.Request) (*fetch.Response, error)
}

// throughServer returns what puts the images of an entry behind the address
// of the server, as the installation is set: base goes before the path,
// which for a page is its prefix.
func (h *Handler) throughServer(ctx context.Context, mode, base string) (func(content string) string, error) {
	if h.images == nil || (mode != mediaproxy.ModeHTTPOnly && mode != mediaproxy.ModeAll) {
		return func(content string) string { return content }, nil
	}
	salt, err := h.db.Salt(ctx)
	if err != nil {
		return nil, err
	}
	key := mediaproxy.Key(salt)
	return func(content string) string {
		return mediaproxy.Rewrite(content, mode, func(target string) string { return mediaproxy.Address(key, base, target) })
	}, nil
}

// media hands out the image an address written by the server leads to. The
// signature of the address is the permission: no login is asked for, since
// an app that reads through the API has none to show.
func (h *Handler) media(w http.ResponseWriter, r *http.Request) {
	if h.images == nil {
		http.NotFound(w, r)
		return
	}
	salt, err := h.db.Salt(r.Context())
	if err != nil {
		h.log.Error("media proxy", "error", err)
		http.Error(w, http.StatusText(http.StatusInternalServerError), http.StatusInternalServerError)
		return
	}
	target, ok := mediaproxy.Target(mediaproxy.Key(salt), r.PathValue("digest"), r.PathValue("address"))
	if !ok {
		http.Error(w, http.StatusText(http.StatusForbidden), http.StatusForbidden)
		return
	}
	header := w.Header()
	sum := sha256.Sum256([]byte(target))
	etag := `"` + hex.EncodeToString(sum[:16]) + `"`
	header.Set("Cache-Control", mediaCache)
	header.Set("ETag", etag)
	// The browser has the image: the site is not asked again.
	if r.Header.Get("If-None-Match") == etag {
		w.WriteHeader(http.StatusNotModified)
		return
	}

	resp, err := h.images.Fetch(r.Context(), fetch.Request{URL: target, Accept: acceptImage})
	if err != nil {
		header.Del("Cache-Control")
		header.Del("ETag")
		status := http.StatusBadGateway
		var answered *fetch.StatusError
		switch {
		case errors.Is(err, fetch.ErrForbiddenAddress):
			status = http.StatusForbidden
		case errors.As(err, &answered) && (answered.Code == http.StatusNotFound || answered.Code == http.StatusGone):
			status = http.StatusNotFound
		}
		h.log.Debug("media proxy", "error", err)
		http.Error(w, http.StatusText(status), status)
		return
	}
	kind, _, err := mime.ParseMediaType(resp.Header.Get("Content-Type"))
	if err != nil || !strings.HasPrefix(kind, "image/") {
		header.Del("Cache-Control")
		header.Del("ETag")
		http.Error(w, http.StatusText(http.StatusUnsupportedMediaType), http.StatusUnsupportedMediaType)
		return
	}
	header.Set("Content-Type", resp.Header.Get("Content-Type"))
	header.Set("Content-Length", strconv.Itoa(len(resp.Body)))
	header.Set("Content-Security-Policy", mediaSecurityPolicy)
	_, _ = w.Write(resp.Body)
}
