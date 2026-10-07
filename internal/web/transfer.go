package web

import (
	"archive/zip"
	"bytes"
	"context"
	"errors"
	"io"
	"net/http"
	"strconv"

	"github.com/juev/freshgo/internal/opml"
	"github.com/juev/freshgo/internal/store"
	"github.com/juev/freshgo/internal/transfer"
)

// maxImport bounds a file a user hands over for import, in bytes.
const maxImport = 64 << 20

// transferPage is what the page of import and export shows.
type transferPage struct {
	Feeds   []option
	Problem string
}

func (h *Handler) showTransfer(w http.ResponseWriter, r *http.Request, status int, problem string) {
	v := h.view(r, "subscriptions", "transfer.heading")
	feeds, err := h.db.Feeds(r.Context(), state(r).who.user.ID)
	if err != nil {
		h.broken(w, r, err)
		return
	}
	page := transferPage{}
	for _, f := range feeds {
		page.Feeds = append(page.Feeds, option{Value: strconv.FormatInt(f.ID, 10), Name: f.Name})
	}
	if problem != "" {
		page.Problem = v.T(problem)
	}
	v.Data = page
	h.render(w, r, status, "transfer", v)
}

func (h *Handler) transferPage(w http.ResponseWriter, r *http.Request) {
	h.showTransfer(w, r, http.StatusOK, "")
}

// importFile takes in the file the form brings: subscriptions, entries, or
// an archive of both.
func (h *Handler) importFile(w http.ResponseWriter, r *http.Request) {
	ctx, s := r.Context(), state(r)
	r.Body = http.MaxBytesReader(w, r.Body, maxImport)
	if err := r.ParseMultipartForm(maxImport); err != nil || !isText(r.PostForm) {
		var tooLarge *http.MaxBytesError
		if errors.As(err, &tooLarge) {
			h.showTransfer(w, r, http.StatusRequestEntityTooLarge, "transfer.problem.large")
			return
		}
		h.fail(w, r, http.StatusBadRequest)
		return
	}
	defer func() { _ = r.MultipartForm.RemoveAll() }()
	files := r.MultipartForm.File["file"]
	if len(files) == 0 || files[0].Size == 0 {
		h.showTransfer(w, r, http.StatusBadRequest, "transfer.problem.none")
		return
	}
	file, err := files[0].Open()
	if err != nil {
		h.broken(w, r, err)
		return
	}
	data, err := io.ReadAll(file)
	_ = file.Close()
	if err != nil {
		h.broken(w, r, err)
		return
	}
	report, err := transfer.Import(ctx, h.db, h.hooks, s.who.user, files[0].Filename, data, transfer.Options{
		MaxFeeds: s.system.Limits.MaxFeeds, MaxCategories: s.system.Limits.MaxCategories, Now: h.now(),
	})
	switch {
	case errors.Is(err, transfer.ErrUnknownFile):
		h.showTransfer(w, r, http.StatusBadRequest, "transfer.problem.unknown")
		return
	case errors.Is(err, transfer.ErrDocument), errors.Is(err, opml.ErrDocument):
		h.showTransfer(w, r, http.StatusBadRequest, "transfer.problem.unreadable")
		return
	case err != nil:
		h.broken(w, r, err)
		return
	}
	notice := "notice.imported"
	if report.Incomplete {
		notice = "notice.imported-partly"
	}
	h.notify(w, r, notice, report.Feeds+report.Entries+report.Updated)
	http.Redirect(w, r, h.url("/subscriptions/transfer"), http.StatusSeeOther)
}

// exported is a file of an export: its name and what writes it.
type exported struct {
	name  string
	write func(ctx context.Context, w io.Writer) error
}

// exportFiles answers with what the form asks for: the subscriptions as
// OPML, the starred and the labelled entries, the entries of feeds. One
// file goes out as it is, several in a ZIP archive.
func (h *Handler) exportFiles(w http.ResponseWriter, r *http.Request) {
	ctx, user := r.Context(), state(r).who.user
	if !h.form(w, r) {
		return
	}
	v := h.view(r, "", "transfer.heading")
	day := h.now().In(readReading(user).location()).Format("2006-01-02")
	var files []exported
	if r.PostForm.Get("opml") != "" {
		files = append(files, exported{"feeds_" + day + ".opml.xml", func(ctx context.Context, w io.Writer) error {
			document, err := opml.Export(ctx, h.db, user, h.now())
			if err == nil {
				_, err = w.Write(document)
			}
			return err
		}})
	}
	starred, labelled := r.PostForm.Get("starred") != "", r.PostForm.Get("labelled") != ""
	if starred || labelled {
		// The two go into one document: an entry may be both.
		set := store.EntrySet{OnlyFavorite: starred && !labelled, Labeled: labelled && !starred, FavoriteOrLabeled: starred && labelled}
		files = append(files, exported{"starred_" + day + ".json", func(ctx context.Context, w io.Writer) error {
			return transfer.Write(ctx, w, h.db, h.hooks, user, transfer.Document{Kind: "starred", Title: v.T("transfer.starred-title"), Set: set})
		}})
	}
	feeds, err := h.db.Feeds(ctx, user.ID)
	if err != nil {
		h.broken(w, r, err)
		return
	}
	wanted := map[string]bool{}
	for _, id := range r.PostForm["feed"] {
		wanted[id] = true
	}
	for _, f := range feeds {
		id := strconv.FormatInt(f.ID, 10)
		if !wanted[id] {
			continue
		}
		name := "feed_" + day + "_" + strconv.FormatInt(f.CategoryID, 10) + "_" + id + ".json"
		files = append(files, exported{name, func(ctx context.Context, w io.Writer) error {
			return transfer.Write(ctx, w, h.db, h.hooks, user, transfer.Document{
				Kind: "feed/" + id, Title: v.T("transfer.feed-title", f.Name), Set: store.EntrySet{FeedID: f.ID},
			})
		}})
	}
	if len(files) == 0 {
		h.showTransfer(w, r, http.StatusBadRequest, "transfer.problem.nothing")
		return
	}

	// The answer is made in full first: a failure half way is then an error
	// page, not half a file.
	var out bytes.Buffer
	name, contentType := files[0].name, "application/json; charset=utf-8"
	if len(files) == 1 {
		if name[len(name)-4:] == ".xml" {
			contentType = "application/xml; charset=utf-8"
		}
		err = files[0].write(ctx, &out)
	} else {
		name, contentType = "freshgo_"+user.Name+"_"+day+"_export.zip", "application/zip"
		archive := zip.NewWriter(&out)
		for _, f := range files {
			var member io.Writer
			if member, err = archive.Create(f.name); err == nil {
				err = f.write(ctx, member)
			}
			if err != nil {
				break
			}
		}
		if err == nil {
			err = archive.Close()
		}
	}
	if err != nil {
		h.broken(w, r, err)
		return
	}
	header := w.Header()
	header.Set("Content-Type", contentType)
	header.Set("Content-Disposition", `attachment; filename="`+name+`"`)
	header.Set("Cache-Control", "no-store")
	_, _ = out.WriteTo(w)
}
