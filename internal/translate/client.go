package translate

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/juev/freshgo/internal/fetch"
)

// Fetcher makes the requests: the client of freshgo, with its address
// rules and limits.
type Fetcher interface {
	Fetch(ctx context.Context, r fetch.Request) (*fetch.Response, error)
}

// Client asks a model behind an OpenAI-compatible chat endpoint. The
// request has the model and the messages and nothing else: the model
// answers as the service has it by default.
type Client struct {
	// URL is the address of the API, without "/chat/completions".
	URL, Key, Model string
	HTTP            Fetcher
}

// answerTimeout is how long one answer may take: a model that reasons
// needs minutes for a long request.
const answerTimeout = 3 * time.Minute

// Stats say how the paragraphs of a text fared and what the answers cost.
type Stats struct {
	// Paragraphs were sent; Again of them were asked for a second time,
	// alone, for the Reasons counted; Failed stand as they were.
	Paragraphs, Again, Failed int
	Reasons                   map[string]int
	Requests, In, Out         int
	// Cost is what the service says the answers cost, where it says so.
	Cost float64
}

func (s *Stats) why(reason string) {
	if s.Reasons == nil {
		s.Reasons = map[string]int{}
	}
	s.Reasons[reason]++
}

var fence = regexp.MustCompile("(?s)^\\s*```[a-zA-Z]*\\s*\n(.*?)\\s*```\\s*$")

func (c *Client) ask(ctx context.Context, system, user string, st *Stats) (string, error) {
	body, err := json.Marshal(map[string]any{
		"model":    c.Model,
		"messages": []map[string]string{{"role": "system", "content": system}, {"role": "user", "content": user}},
	})
	if err != nil {
		return "", err
	}
	resp, err := c.HTTP.Fetch(ctx, fetch.Request{
		URL: strings.TrimSuffix(c.URL, "/") + "/chat/completions", Accept: "application/json",
		Params: fetch.Params{
			Post: true, PostBody: string(body), Timeout: answerTimeout, NoRedirects: true,
			Header: http.Header{"Content-Type": {"application/json"}, "Authorization": {"Bearer " + c.Key}},
		},
	})
	st.Requests++
	if err != nil {
		return "", fmt.Errorf("translate: %w", err)
	}
	var answer struct {
		Choices []struct {
			Message struct {
				Content string `json:"content"`
			} `json:"message"`
			FinishReason string `json:"finish_reason"`
		} `json:"choices"`
		Usage struct {
			PromptTokens     int     `json:"prompt_tokens"`
			CompletionTokens int     `json:"completion_tokens"`
			Cost             float64 `json:"cost"`
		} `json:"usage"`
	}
	if err := json.Unmarshal(resp.Body, &answer); err != nil {
		return "", fmt.Errorf("translate: %w", err)
	}
	st.In, st.Out, st.Cost = st.In+answer.Usage.PromptTokens, st.Out+answer.Usage.CompletionTokens, st.Cost+answer.Usage.Cost
	if len(answer.Choices) == 0 {
		return "", errors.New("translate: no answer")
	}
	if reason := answer.Choices[0].FinishReason; reason != "stop" {
		return "", fmt.Errorf("translate: the answer ended by %q", reason)
	}
	content := answer.Choices[0].Message.Content
	if m := fence.FindStringSubmatch(content); m != nil {
		content = m[1]
	}
	return strings.TrimSpace(content), nil
}

const (
	// batchUnits and batchBytes bound what goes into one request.
	batchUnits = 40
	batchBytes = 3000
)

func rules(language string) string {
	return "Translate into " + language + ", whatever language the text is in. " +
		"Marks like <1>…</1> and <2/> stand for links, emphasis, images and code: keep every one of them, each exactly once, around the words it was around; " +
		"they may move as the order of words changes. Do not translate names of products and code. Answer with the translation alone."
}

var numbered = regexp.MustCompile(`(?m)^\s*\[\[(\d+)\]\][ \t]*`)

// first returns how many of the units make the next request.
func first(all []*unit) int {
	n, size := 0, 0
	for n < len(all) && n < batchUnits && (n == 0 || size+len(all[n].marked) <= batchBytes) {
		size += len(all[n].marked)
		n++
	}
	return n
}

// translate puts the translations of the paragraphs in their place. They
// are asked for together, each under its number, and the answer is taken
// paragraph by paragraph; a paragraph whose answer cannot be used is asked
// for again alone, and stands as it was when that answer is no better. A
// request that fails is an error: the paragraphs are then to be asked for
// another time, not given up.
func (c *Client) translate(ctx context.Context, batch []*unit, language string, st *Stats) error {
	together := "You are a translator. The user sends paragraphs of one article, each on a line that begins with its number in double square brackets, like [[7]]. " +
		rules(language) + " Answer in the same form: each translated paragraph on a line that begins with its number in double square brackets. Translate every paragraph."
	if len(batch) == 0 {
		return nil
	}
	st.Paragraphs += len(batch)
	var request strings.Builder
	for i, one := range batch {
		fmt.Fprintf(&request, "[[%d]] %s\n\n", i+1, one.marked)
	}
	answer, err := c.ask(ctx, together, request.String(), st)
	if err != nil {
		return err
	}
	answers := map[int]string{}
	found := numbered.FindAllStringSubmatchIndex(answer, -1)
	for i, loc := range found {
		end := len(answer)
		if i+1 < len(found) {
			end = found[i+1][0]
		}
		number, _ := strconv.Atoi(answer[loc[2]:loc[3]])
		if _, twice := answers[number]; !twice {
			answers[number] = answer[loc[1]:end]
		}
	}
	for i, one := range batch {
		reason := "no answer"
		if got, ok := answers[i+1]; ok {
			if reason = "left untranslated"; !one.same(got) {
				reason = one.put(got)
			}
		}
		if reason == "" {
			continue
		}
		st.Again++
		st.why(reason)
		got, err := c.alone(ctx, one.marked, language, st)
		if err != nil {
			return err
		}
		// A paragraph the model leaves as it is when asked for it alone
		// needs no translating.
		if !one.same(got) && one.put(got) != "" {
			st.Failed++
		}
	}
	return nil
}

// alone asks for the translation of one paragraph.
func (c *Client) alone(ctx context.Context, paragraph, language string, st *Stats) (string, error) {
	return c.ask(ctx, "You are a translator. The user sends one paragraph of an article. "+rules(language), paragraph, st)
}

// written asks whether a text is in a language already.
func (c *Client) written(ctx context.Context, sample, language string, st *Stats) (bool, error) {
	answer, err := c.ask(ctx, "Answer with one word: yes or no.", "Is the following text written in "+language+"?\n\n"+sample, st)
	return strings.HasPrefix(strings.ToLower(answer), "yes"), err
}

// Blocks translates an HTML fragment, all of it.
func (c *Client) Blocks(ctx context.Context, fragment, language string) (string, Stats, error) {
	root, err := parse(fragment)
	if err != nil {
		return "", Stats{}, err
	}
	var st Stats
	for all := units(root); len(all) > 0; {
		n := first(all)
		if err := c.translate(ctx, all[:n], language, &st); err != nil {
			return "", st, err
		}
		all = all[n:]
	}
	return render(root), st, nil
}
