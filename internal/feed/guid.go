package feed

import (
	"bufio"
	"crypto/sha1" //nolint:gosec // the key format of FreshRSS, not a security measure
	_ "embed"
	"encoding/hex"
	"math"
	"regexp"
	"strconv"
	"strings"
)

//go:embed force-https.default.txt
var defaultHTTPSDomains string

// HTTPSDomains is the list of domains whose http:// URLs are rewritten to
// https://, subdomains included. It affects entry identifiers, so it has to
// stay what it was in the FreshRSS installation the data came from.
type HTTPSDomains struct {
	root domainNode
}

type domainNode struct {
	listed   bool
	children map[string]*domainNode
}

var domainNoise = regexp.MustCompile(`\s+|[/#;].*$`)

// NewHTTPSDomains returns the list FreshRSS ships plus the given domains,
// which come from the force-https.txt of an installation.
func NewHTTPSDomains(extra []string) *HTTPSDomains {
	h := &HTTPSDomains{}
	sc := bufio.NewScanner(strings.NewReader(defaultHTTPSDomains))
	for sc.Scan() {
		h.add(sc.Text())
	}
	for _, line := range extra {
		h.add(line)
	}
	return h
}

func (h *HTTPSDomains) add(line string) {
	domain := strings.Trim(domainNoise.ReplaceAllString(line, ""), ". \t\n\r\x00\x0B")
	if domain == "" {
		return
	}
	n := &h.root
	segments := strings.Split(domain, ".")
	for i := len(segments) - 1; i >= 0 && !n.listed; i-- {
		if n.children == nil {
			n.children = map[string]*domainNode{}
		}
		child := n.children[segments[i]]
		if child == nil {
			child = &domainNode{}
			n.children[segments[i]] = child
		}
		n = child
	}
	n.listed = true
	n.children = nil
}

func (h *HTTPSDomains) contains(host string) bool {
	n := &h.root
	segments := strings.Split(strings.Trim(host, ". "), ".")
	for i := len(segments) - 1; i >= 0; i-- {
		child := n.children[segments[i]]
		if child == nil {
			break
		}
		n = child
	}
	return n.listed
}

// URL returns u with https:// in place of http:// when its host is listed.
func (h *HTTPSDomains) URL(u string) string {
	if len(u) < 7 || !strings.EqualFold(u[:7], "http://") {
		return u
	}
	host := u[7:]
	if i := strings.IndexAny(host, "/?#"); i >= 0 {
		host = host[:i]
	}
	if i := strings.LastIndexByte(host, '@'); i >= 0 {
		host = host[i+1:]
	}
	if i := strings.LastIndexByte(host, ':'); i >= 0 && !strings.Contains(host[i:], "]") {
		// PHP parse_url rejects the URL for a port number that is too large.
		if port, err := strconv.ParseUint(host[i+1:], 10, 64); err == nil && port > 65535 {
			return u
		}
		host = host[:i]
	}
	if host == "" || !h.contains(host) {
		return u
	}
	return u[:4] + "s" + u[4:]
}

// Criteria of entry uniqueness: the values of feed.attributes.unicityCriteria.
// The empty one is the identifier the feed gives.
const (
	CriteriaID                 = ""
	criteriaLink               = "link"
	criteriaLinkPublished      = "sha1:link_published"
	criteriaLinkPublishedTitle = "sha1:link_published_title"
)

const blankSHA1 = "da39a3ee5e6b4b0d3255bfef95601890afd80709"

// maxGUIDLength is the size of the key column in FreshRSS.
const maxGUIDLength = 767

func sha1Hex(parts ...string) string {
	sum := sha1.Sum([]byte(strings.Join(parts, ""))) //nolint:gosec // see the import
	return hex.EncodeToString(sum[:])
}

// safeASCII drops control characters and everything outside ASCII.
func safeASCII(s string) string {
	b := make([]byte, 0, len(s))
	for i := 0; i < len(s); i++ {
		if c := s[i]; c >= 32 && c <= 127 {
			b = append(b, c)
		}
	}
	return string(b)
}

// guid is FreshRSS_Feed::decideEntryGuid with the fallback on.
func (it *Item) guid(criteria string) string {
	id := safeASCII(it.id)
	// PHP takes "0" for no value.
	if id == "0" {
		id = ""
	}
	var guid string
	switch criteria {
	case criteriaLink:
		guid = it.permalink
	case criteriaLinkPublished:
		guid = sha1Hex(it.permalink, it.date)
	case criteriaLinkPublishedTitle:
		guid = sha1Hex(it.permalink, it.date, it.title)
	case "sha1:link_published_title_content":
		guid = sha1Hex(it.permalink, it.date, it.title, it.content)
	case "sha1:title":
		guid = sha1Hex(it.title)
	case "sha1:title_published":
		guid = sha1Hex(it.title, it.date)
	case "sha1:title_published_content":
		guid = sha1Hex(it.title, it.date, it.content)
	case "sha1:content":
		guid = sha1Hex(it.content)
	case "sha1:content_published":
		guid = sha1Hex(it.content, it.date)
	case "sha1:published":
		guid = sha1Hex(it.date)
	default:
		guid = id
	}
	if guid == blankSHA1 {
		guid = ""
	}
	if guid == "" {
		switch {
		case id != "":
			guid = id
		case it.permalink != "":
			guid = sha1Hex(it.permalink, it.date)
		case it.title != "":
			guid = sha1Hex(it.permalink, it.date, it.title)
		default:
			guid = sha1Hex(it.permalink, it.date, it.title, it.content)
		}
		if guid == blankSHA1 {
			guid = ""
		}
	}
	// FreshRSS trims the key when it stores it.
	guid = phpTrim(guid)
	if len(guid) > maxGUIDLength {
		guid = guid[:maxGUIDLength]
	}
	return guid
}

// AssignGUIDs sets the GUID of every item by the feed's uniqueness criteria
// (feed.attributes.unicityCriteria; legacy hasBadGuids means "link").
//
// When more than 5 % of the items come out with an empty or a repeated GUID,
// the criteria degrade to the next weaker one, unless forced: the returned
// criteria are then the ones to store with the feed. invalid is the number
// of bad GUIDs under the returned criteria; FreshRSS marks the feed as
// failing when it is not zero.
func (f *Feed) AssignGUIDs(criteria string, forced bool) (used string, invalid int) {
	for {
		seen := map[string]bool{}
		invalid = 0
		for _, it := range f.Items {
			it.GUID = it.guid(criteria)
			if it.GUID == "" || seen[it.GUID] {
				invalid++
			}
			seen[it.GUID] = true
		}
		if invalid == 0 || forced || float64(invalid) <= math.Round(0.05*float64(len(f.Items))) {
			return criteria, invalid
		}
		switch criteria {
		case CriteriaID, criteriaLink:
			criteria = criteriaLinkPublished
		case criteriaLinkPublished:
			criteria = criteriaLinkPublishedTitle
		default:
			return criteria, invalid
		}
	}
}
