package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os/exec"
	"strings"
)

// GitHub review in workspace mode: each checkout's branch is matched to its
// pull request, whose review state, checks and threads px0 shows and answers.

// gitConfig reads one git config value in root ("" when unset).
func gitConfig(root, key string) string {
	out, err := exec.Command("git", "-C", root, "config", "--get", key).Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}

// githubOwnerRepo parses a remote URL into a GitHub owner and repo.
func githubOwnerRepo(remoteURL string) (owner, repo string, ok bool) {
	host, path, ok := remoteHostPath(remoteURL)
	if !ok || host != "github.com" {
		return "", "", false
	}
	owner, repo, found := strings.Cut(path, "/")
	if !found || owner == "" || repo == "" || strings.Contains(repo, "/") {
		return "", "", false
	}
	return owner, repo, true
}

// prRepoFor names where a checkout's pull request lives and who owns its head
// branch. The PR repo is the upstream remote when there is one (a fork's PRs
// are opened against it), else origin. The head owner is the owner of the
// remote the branch is pushed to: its tracking remote, else origin.
func prRepoFor(root, branch string) (owner, repo, headOwner string, ok bool) {
	prRemote := "upstream"
	u := gitConfig(root, "remote.upstream.url")
	if u == "" {
		prRemote, u = "origin", gitConfig(root, "remote.origin.url")
	}
	owner, repo, ok = githubOwnerRepo(u)
	if !ok {
		return "", "", "", false
	}
	pushRemote := gitConfig(root, "branch."+branch+".remote")
	if pushRemote == "" || pushRemote == "." {
		pushRemote = "origin"
	}
	headOwner = owner
	if pushRemote != prRemote {
		if ho, _, hok := githubOwnerRepo(gitConfig(root, "remote."+pushRemote+".url")); hok {
			headOwner = ho
		}
	}
	return owner, repo, headOwner, true
}

// githubGraphQL runs one GraphQL request and decodes its data into out. A 200
// carrying errors is an error, never an empty result.
func githubGraphQL(ctx context.Context, token, query string, vars map[string]any, out any) error {
	b, err := json.Marshal(map[string]any{"query": query, "variables": vars})
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, githubGraphQLURL, bytes.NewReader(b))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	resp, err := githubHTTPClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode/100 != 2 {
		return fmt.Errorf("github graphql: %s", resp.Status)
	}
	var env struct {
		Data   json.RawMessage `json:"data"`
		Errors []struct {
			Message string `json:"message"`
		} `json:"errors"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&env); err != nil {
		return err
	}
	if len(env.Errors) > 0 {
		return errors.New("github graphql: " + env.Errors[0].Message)
	}
	return json.Unmarshal(env.Data, out)
}

// PRSummary is a checkout's pull request as the explorer shows it.
type PRSummary struct {
	Number     int    `json:"number"`
	Title      string `json:"title"`
	URL        string `json:"url"`
	State      string `json:"state"` // OPEN | CLOSED | MERGED
	Draft      bool   `json:"draft,omitempty"`
	Review     string `json:"review"` // APPROVED | CHANGES_REQUESTED | REVIEW_REQUIRED | ""
	Checks     string `json:"checks"` // SUCCESS | FAILURE | PENDING | ERROR | EXPECTED | ""
	Unresolved int    `json:"unresolved"`
	HeadOID    string `json:"headOid"`
}

// ghPR is the GraphQL shape of a pull request in the summary query.
type ghPR struct {
	Number              int    `json:"number"`
	Title               string `json:"title"`
	URL                 string `json:"url"`
	State               string `json:"state"`
	IsDraft             bool   `json:"isDraft"`
	ReviewDecision      string `json:"reviewDecision"`
	HeadRefOid          string `json:"headRefOid"`
	HeadRepositoryOwner struct {
		Login string `json:"login"`
	} `json:"headRepositoryOwner"`
	Commits struct {
		Nodes []struct {
			Commit struct {
				StatusCheckRollup *struct {
					State string `json:"state"`
				} `json:"statusCheckRollup"`
			} `json:"commit"`
		} `json:"nodes"`
	} `json:"commits"`
	ReviewThreads struct {
		Nodes []struct {
			IsResolved bool `json:"isResolved"`
			IsOutdated bool `json:"isOutdated"`
		} `json:"nodes"`
	} `json:"reviewThreads"`
}

func (p ghPR) summary() PRSummary {
	s := PRSummary{Number: p.Number, Title: p.Title, URL: p.URL, State: p.State, Draft: p.IsDraft, Review: p.ReviewDecision, HeadOID: p.HeadRefOid}
	if n := p.Commits.Nodes; len(n) > 0 && n[0].Commit.StatusCheckRollup != nil {
		s.Checks = n[0].Commit.StatusCheckRollup.State
	}
	for _, t := range p.ReviewThreads.Nodes {
		if !t.IsResolved && !t.IsOutdated {
			s.Unresolved++
		}
	}
	return s
}

const prSummaryFields = `nodes { number title url state isDraft reviewDecision headRefOid
  headRepositoryOwner { login }
  commits(last: 1) { nodes { commit { statusCheckRollup { state } } } }
  reviewThreads(first: 100) { nodes { isResolved isOutdated } } }`

// prAliasBatch bounds the branches looked up in one GraphQL request.
const prAliasBatch = 20

// fetchPRSummaries finds the most recent pull request for each branch in
// owner/repo whose head is owned by headOwner (a same-named branch from
// another fork is not ours). Branches without one are absent from the map.
func fetchPRSummaries(ctx context.Context, token, owner, repo, headOwner string, branches []string) (map[string]PRSummary, error) {
	out := map[string]PRSummary{}
	for start := 0; start < len(branches); start += prAliasBatch {
		chunk := branches[start:min(start+prAliasBatch, len(branches))]
		var q strings.Builder
		q.WriteString("query($owner: String!, $repo: String!")
		vars := map[string]any{"owner": owner, "repo": repo}
		for i, b := range chunk {
			fmt.Fprintf(&q, ", $h%d: String!", i)
			vars[fmt.Sprintf("h%d", i)] = b
		}
		q.WriteString(") { repository(owner: $owner, name: $repo) {")
		for i := range chunk {
			fmt.Fprintf(&q, " b%d: pullRequests(headRefName: $h%d, first: 3, orderBy: {field: UPDATED_AT, direction: DESC}) { %s }", i, i, prSummaryFields)
		}
		q.WriteString(" } }")
		var data struct {
			Repository map[string]struct {
				Nodes []ghPR `json:"nodes"`
			} `json:"repository"`
		}
		if err := githubGraphQL(ctx, token, q.String(), vars, &data); err != nil {
			return nil, err
		}
		for i, b := range chunk {
			for _, pr := range data.Repository[fmt.Sprintf("b%d", i)].Nodes {
				if strings.EqualFold(pr.HeadRepositoryOwner.Login, headOwner) {
					out[b] = pr.summary()
					break
				}
			}
		}
	}
	return out, nil
}
