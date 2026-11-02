package greader

import (
	"context"
	"reflect"
	"strconv"
	"testing"

	"github.com/juev/freshgo/internal/store"
)

// user/-/label/<name> finds the category or the label of the name whatever
// the case, Cyrillic included, in streams and in every edit.
func TestLabelNamesIgnoreCase(t *testing.T) {
	eachEngine(t, func(t *testing.T, w *world) {
		ctx := context.Background()
		for _, name := range []string{"News", "Новости"} {
			if err := w.db.CreateCategory(ctx, &store.Category{UserID: w.alice.ID, Name: name}); err != nil {
				t.Fatal(err)
			}
		}
		categories, err := w.db.Categories(ctx, w.alice.ID)
		if err != nil {
			t.Fatal(err)
		}
		news, russian := categories[1], categories[2]
		for _, name := range []string{"Keep", "Важное"} {
			if err := w.db.CreateTag(ctx, &store.Tag{UserID: w.alice.ID, Name: name}); err != nil {
				t.Fatal(err)
			}
		}
		a := w.feed(w.alice, &store.Feed{Name: "A", Priority: priorityMain, CategoryID: news.ID})
		b := w.feed(w.alice, &store.Feed{Name: "B", Priority: priorityMain, CategoryID: russian.ID})
		w.entries(w.alice,
			&store.Entry{ID: 1 * second, FeedID: a.ID, Title: "a"},
			&store.Entry{ID: 2 * second, FeedID: b.ID, Title: "b"})

		if got := w.ids("n=10&s=user/-/label/news"); !reflect.DeepEqual(got, []string{"1000000"}) {
			t.Errorf("stream of label/news = %v, want the entry of the category News", got)
		}
		if got := w.ids("n=10&s=user/-/label/НОВОСТИ"); !reflect.DeepEqual(got, []string{"2000000"}) {
			t.Errorf("stream of label/НОВОСТИ = %v, want the entry of the category Новости", got)
		}

		// Labels of another case are the existing labels, not new ones, and a
		// name a category has makes no label.
		w.ok("/reader/api/0/edit-tag", "a=user/-/label/keep&a=user/-/label/важное&a=user/-/label/NEWS&i=1000000")
		tags, err := w.db.Tags(ctx, w.alice.ID)
		if err != nil || len(tags) != 2 {
			t.Fatalf("labels = %v, %v; want Keep and Важное only", tags, err)
		}
		if got := w.ids("n=10&s=user/-/label/KEEP"); !reflect.DeepEqual(got, []string{"1000000"}) {
			t.Errorf("stream of label/KEEP = %v, want the entry that was labelled keep", got)
		}
		w.ok("/reader/api/0/edit-tag", "r=user/-/label/KEEP&i=1000000")
		if got := w.ids("n=10&s=user/-/label/Keep"); len(got) != 0 {
			t.Errorf("stream of label/Keep after removing KEEP = %v, want it empty", got)
		}

		// A feed moved to a category of another case goes to the one there is.
		w.ok("/reader/api/0/subscription/edit", "ac=edit&s=feed/"+strconv.FormatInt(b.ID, 10)+"&a=user/-/label/news")
		if got, err := w.db.FeedByID(ctx, w.alice.ID, b.ID); err != nil || got.CategoryID != news.ID {
			t.Errorf("feed moved to label/news = %+v, %v; want the category News", got, err)
		}
		if categories, err = w.db.Categories(ctx, w.alice.ID); err != nil || len(categories) != 3 {
			t.Errorf("%d categories, %v; want no new one", len(categories), err)
		}

		// rename-tag and disable-tag find the category by any case.
		w.ok("/reader/api/0/rename-tag", "s=user/-/label/NEWS&dest=user/-/label/Headlines")
		if got, err := w.db.Categories(ctx, w.alice.ID); err != nil || got[1].Name != "Headlines" {
			t.Errorf("categories after renaming NEWS = %v, %v; want News renamed", got, err)
		}
		w.ok("/reader/api/0/disable-tag", "s=user/-/label/новости")
		if got, err := w.db.Categories(ctx, w.alice.ID); err != nil || len(got) != 2 {
			t.Errorf("categories after disabling новости = %v, %v; want it gone", got, err)
		}
		w.ok("/reader/api/0/rename-tag", "s=user/-/label/ВАЖНОЕ&dest=user/-/label/Urgent")
		if tags, err = w.db.Tags(ctx, w.alice.ID); err != nil || tags[1].Name != "Urgent" {
			t.Errorf("labels after renaming ВАЖНОЕ = %v, %v; want Важное renamed", tags, err)
		}
	})
}

// With a category and a label that differ only in case, as a FreshRSS import
// can store, the exact name wins whichever of the two has it; any other case
// is the category.
func TestExactLabelNameWinsOverCategory(t *testing.T) {
	eachEngine(t, func(t *testing.T, w *world) {
		ctx := context.Background()
		exact := w.db.WithExactNames()
		category := &store.Category{UserID: w.alice.ID, Name: "News"}
		label := &store.Tag{UserID: w.alice.ID, Name: "news"}
		if err := exact.CreateCategory(ctx, category); err != nil {
			t.Fatal(err)
		}
		if err := exact.CreateTag(ctx, label); err != nil {
			t.Fatal(err)
		}
		f := w.feed(w.alice, &store.Feed{Name: "A", Priority: priorityMain, CategoryID: category.ID})
		w.entries(w.alice,
			&store.Entry{ID: 1 * second, FeedID: f.ID, Title: "in the category"},
			&store.Entry{ID: 2 * second, FeedID: f.ID, Title: "labelled"})
		if err := w.db.TagEntry(ctx, w.alice.ID, label.ID, 2*second); err != nil {
			t.Fatal(err)
		}

		for name, want := range map[string][]string{
			"news": {"2000000"},
			"News": {"2000000", "1000000"},
			"NEWS": {"2000000", "1000000"},
		} {
			if got := w.ids("n=10&s=user/-/label/" + name); !reflect.DeepEqual(got, want) {
				t.Errorf("stream of label/%s = %v, want %v", name, got, want)
			}
		}

		// rename-tag and disable-tag resolve the name alike.
		w.ok("/reader/api/0/rename-tag", "s=user/-/label/news&dest=user/-/label/Later")
		tags, err := w.db.Tags(ctx, w.alice.ID)
		if err != nil || len(tags) != 1 || tags[0].Name != "Later" {
			t.Errorf("labels after renaming label/news = %v, %v; want the label renamed", tags, err)
		}
		w.ok("/reader/api/0/disable-tag", "s=user/-/label/NEWS")
		categories, err := w.db.Categories(ctx, w.alice.ID)
		if err != nil || len(categories) != 1 {
			t.Errorf("categories after disabling label/NEWS = %v, %v; want the category gone", categories, err)
		}
	})
}
