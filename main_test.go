package main

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"
)

func TestSafeName(t *testing.T) {
	for _, value := range []string{"repo", "repo.name", "with-dash", "with_underscore"} {
		if !safeName(value) {
			t.Errorf("safeName(%q) = false", value)
		}
	}
	for _, value := range []string{"", ".", "..", "a/b", `a\\b`} {
		if safeName(value) {
			t.Errorf("safeName(%q) = true", value)
		}
	}
}

func TestOwnedRepositoriesFiltersAndPaginates(t *testing.T) {
	pages := 0
	transport := roundTripFunc(func(r *http.Request) (*http.Response, error) {
		pages++
		if r.Header.Get("Authorization") != "Bearer secret" {
			t.Errorf("unexpected Authorization header")
		}
		var repos []repository
		if r.URL.Query().Get("page") == "1" {
			for i := 0; i < 99; i++ {
				repos = append(repos, testRepo("Me/repo"))
			}
			repos = append(repos, testRepo("someone/other"))
		} else {
			repos = append(repos, testRepo("ME/final"))
		}
		var body strings.Builder
		_ = json.NewEncoder(&body).Encode(repos)
		return &http.Response{
			StatusCode: http.StatusOK,
			Status:     "200 OK",
			Body:       io.NopCloser(strings.NewReader(body.String())),
			Header:     make(http.Header),
		}, nil
	})

	client := &githubClient{baseURL: "https://example.invalid", token: "secret", http: &http.Client{Transport: transport}}
	repos, err := client.ownedRepositories(context.Background(), "me")
	if err != nil {
		t.Fatal(err)
	}
	if pages != 2 || len(repos) != 100 {
		t.Fatalf("pages=%d repos=%d, want pages=2 repos=100", pages, len(repos))
	}
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func testRepo(fullName string) repository {
	var repo repository
	repo.FullName = fullName
	repo.Owner.Login, _, _ = cut(fullName)
	return repo
}

func cut(s string) (string, string, bool) {
	for i := range s {
		if s[i] == '/' {
			return s[:i], s[i+1:], true
		}
	}
	return s, "", false
}
