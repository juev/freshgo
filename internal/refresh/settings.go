package refresh

import (
	"encoding/json"
	"time"
)

// defaultTTL is the refresh period of a user who has not set one: the
// ttl_default of FreshRSS.
const defaultTTL = 3600

// attributes is a JSON object read key by key, so that one value of an
// unexpected type does not hide the others.
type attributes map[string]json.RawMessage

func readAttributes(raw json.RawMessage) attributes {
	var a attributes
	if json.Unmarshal(raw, &a) != nil || a == nil {
		return attributes{}
	}
	return a
}

// get decodes the value of a key; ok is false when the key is absent or
// holds another type, which is how FreshRSS reads typed attributes.
func get[T any](a attributes, key string) (v T, ok bool) {
	raw, present := a[key]
	if !present || string(raw) == "null" {
		return v, false
	}
	if json.Unmarshal(raw, &v) != nil {
		var zero T
		return zero, false
	}
	return v, true
}

func (a attributes) set(key string, value any) {
	raw, err := json.Marshal(value)
	if err != nil {
		// Only strings and booleans are stored.
		panic(err)
	}
	a[key] = raw
}

func (a attributes) raw() json.RawMessage {
	raw, err := json.Marshal(a)
	if err != nil {
		panic(err)
	}
	return raw
}

// userSettings are the keys of a user's settings a refresh depends on, under
// the names FreshRSS gives them in the user's config.php.
type userSettings struct {
	enabled bool
	// ttlDefault is the refresh period, in seconds, of feeds that set none.
	ttlDefault int
	// markUpdatedUnread makes an entry the feed has changed unread again.
	markUpdatedUnread bool
	// location is the time zone of feed dates written without one.
	location *time.Location
}

func readUserSettings(raw json.RawMessage) userSettings {
	a := readAttributes(raw)
	s := userSettings{enabled: true, ttlDefault: defaultTTL, location: time.Local}
	if v, ok := get[bool](a, "enabled"); ok {
		s.enabled = v
	}
	if v, ok := get[int](a, "ttl_default"); ok && v > 0 {
		s.ttlDefault = v
	}
	s.markUpdatedUnread, _ = get[bool](a, "mark_updated_article_unread")
	if name, _ := get[string](a, "timezone"); name != "" {
		if loc, err := time.LoadLocation(name); err == nil {
			s.location = loc
		}
	}
	return s
}
