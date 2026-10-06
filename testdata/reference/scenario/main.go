// Command scenario drives a reference FreshRSS through its Google Reader API
// to put entries into the states the import tests expect: read, starred,
// labelled. generate.sh runs it after the feeds have been refreshed.
package main

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"
)

const (
	starred = "user/-/state/com.google/starred"
	read    = "user/-/state/com.google/read"
)

// action applies a tag to the entries with the given titles.
type action struct {
	tag    string
	titles []string
}

type user struct {
	name        string
	apiPassword string
	actions     []action
	// readFeeds are titles of feeds marked as read as a whole.
	readFeeds []string
}

var users = []user{
	{
		name: "alice", apiPassword: "alice-api-password",
		actions: []action{
			{starred, []string{"Plain entry", "Opaque guid"}},
			{read, []string{"Permalink guid"}},
			{"user/-/label/later", []string{"Plain entry", "First JSON Feed item"}},
			{"user/-/label/work & play", []string{"Opaque guid"}},
		},
		readFeeds: []string{"No identifiers"},
	},
	{
		name: "bob", apiPassword: "bob-api-password",
		actions: []action{
			{starred, []string{"Permalink guid"}},
			{"user/-/label/later", []string{"Identifier on a force-https domain"}},
		},
		readFeeds: []string{"Atom corpus (bob)"},
	},
}

func main() {
	if len(os.Args) != 2 {
		fmt.Fprintln(os.Stderr, "usage: scenario <FreshRSS base URL>")
		os.Exit(2)
	}
	api := strings.TrimRight(os.Args[1], "/") + "/api/greader.php"
	for _, u := range users {
		if err := run(api, u); err != nil {
			fmt.Fprintf(os.Stderr, "scenario: %s: %v\n", u.name, err)
			os.Exit(1)
		}
	}
}

type client struct {
	api   string
	auth  string
	token string
	http  *http.Client
}

func run(api string, u user) error {
	c := &client{api: api, http: &http.Client{Timeout: 30 * time.Second}}
	login, err := c.get("/accounts/ClientLogin?" + url.Values{"Email": {u.name}, "Passwd": {u.apiPassword}}.Encode())
	if err != nil {
		return fmt.Errorf("login: %w", err)
	}
	for _, line := range strings.Split(login, "\n") {
		if v, ok := strings.CutPrefix(line, "Auth="); ok {
			c.auth = strings.TrimSpace(v)
		}
	}
	if c.auth == "" {
		return fmt.Errorf("login: no Auth in %q", login)
	}
	token, err := c.get("/reader/api/0/token")
	if err != nil {
		return fmt.Errorf("token: %w", err)
	}
	c.token = strings.TrimSpace(token)

	body, err := c.get("/reader/api/0/stream/contents/user/-/state/com.google/reading-list?output=json&n=1000")
	if err != nil {
		return fmt.Errorf("reading list: %w", err)
	}
	var stream struct {
		Items []struct {
			ID    string `json:"id"`
			Title string `json:"title"`
		} `json:"items"`
	}
	if err := json.Unmarshal([]byte(body), &stream); err != nil {
		return fmt.Errorf("reading list: %w", err)
	}
	idByTitle := map[string]string{}
	for _, it := range stream.Items {
		idByTitle[it.Title] = it.ID
	}

	// Feeds come from the subscription list: the reading list leaves out
	// entries of feeds that are shown only in their own view.
	body, err = c.get("/reader/api/0/subscription/list?output=json")
	if err != nil {
		return fmt.Errorf("subscription list: %w", err)
	}
	var list struct {
		Subscriptions []struct {
			ID    string `json:"id"`
			Title string `json:"title"`
		} `json:"subscriptions"`
	}
	if err := json.Unmarshal([]byte(body), &list); err != nil {
		return fmt.Errorf("subscription list: %w", err)
	}
	feedByTitle := map[string]string{}
	for _, sub := range list.Subscriptions {
		feedByTitle[sub.Title] = sub.ID
	}

	for _, a := range u.actions {
		form := url.Values{"T": {c.token}, "a": {a.tag}}
		for _, title := range a.titles {
			id, ok := idByTitle[title]
			if !ok {
				return fmt.Errorf("no entry titled %q", title)
			}
			form.Add("i", id)
		}
		if err := c.post("/reader/api/0/edit-tag", form); err != nil {
			return fmt.Errorf("edit-tag %s: %w", a.tag, err)
		}
	}
	for _, title := range u.readFeeds {
		stream, ok := feedByTitle[title]
		if !ok {
			return fmt.Errorf("no feed titled %q", title)
		}
		if err := c.post("/reader/api/0/mark-all-as-read", url.Values{"T": {c.token}, "s": {stream}}); err != nil {
			return fmt.Errorf("mark-all-as-read %s: %w", stream, err)
		}
	}
	return nil
}

func (c *client) get(path string) (string, error) {
	req, err := http.NewRequest(http.MethodGet, c.api+path, nil)
	if err != nil {
		return "", err
	}
	return c.do(req)
}

func (c *client) post(path string, form url.Values) error {
	req, err := http.NewRequest(http.MethodPost, c.api+path, strings.NewReader(form.Encode()))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	body, err := c.do(req)
	if err != nil {
		return err
	}
	if strings.TrimSpace(body) != "OK" {
		return fmt.Errorf("unexpected answer %q", body)
	}
	return nil
}

func (c *client) do(req *http.Request) (string, error) {
	if c.auth != "" {
		req.Header.Set("Authorization", "GoogleLogin auth="+c.auth)
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return "", err
	}
	defer func() { _ = resp.Body.Close() }()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", err
	}
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("%s %s: %s: %s", req.Method, req.URL.Path, resp.Status, body)
	}
	return string(body), nil
}
