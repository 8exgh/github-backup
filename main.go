package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"
)

const apiVersion = "2022-11-28"

type repository struct {
	FullName string `json:"full_name"`
	CloneURL string `json:"clone_url"`
	Owner    struct {
		Login string `json:"login"`
	} `json:"owner"`
}

type githubClient struct {
	baseURL string
	token   string
	http    *http.Client
}

func main() {
	if os.Getenv("GITHUB_BACKUP_ASKPASS") == "1" {
		askpass()
		return
	}

	var (
		backupDir = flag.String("dir", "./backups", "directory containing mirror repositories")
		interval  = flag.Duration("interval", 12*time.Hour, "time between backup cycles")
		once      = flag.Bool("once", false, "run one backup cycle and exit")
		apiURL    = flag.String("api-url", "https://api.github.com", "GitHub API base URL")
	)
	flag.Parse()

	if *interval <= 0 {
		log.Fatal("-interval must be greater than zero")
	}
	token := strings.TrimSpace(os.Getenv("GITHUB_TOKEN"))
	if token == "" {
		log.Fatal("GITHUB_TOKEN is required")
	}
	absDir, err := filepath.Abs(*backupDir)
	if err != nil {
		log.Fatalf("resolve backup directory: %v", err)
	}
	if err := os.MkdirAll(absDir, 0o700); err != nil {
		log.Fatalf("create backup directory: %v", err)
	}

	client := &githubClient{
		baseURL: strings.TrimRight(*apiURL, "/"),
		token:   token,
		http:    &http.Client{Timeout: 30 * time.Second},
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	run := func() bool {
		started := time.Now()
		log.Printf("backup cycle started: destination=%s", absDir)
		err := runCycle(ctx, client, absDir)
		if err != nil {
			log.Printf("backup cycle completed with errors after %s: %v", time.Since(started).Round(time.Second), err)
			return false
		}
		log.Printf("backup cycle completed successfully after %s", time.Since(started).Round(time.Second))
		return true
	}

	ok := run()
	if *once {
		if !ok {
			os.Exit(1)
		}
		return
	}

	ticker := time.NewTicker(*interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			log.Printf("shutdown requested")
			return
		case <-ticker.C:
			run()
		}
	}
}

func askpass() {
	prompt := ""
	if len(os.Args) > 1 {
		prompt = strings.ToLower(os.Args[1])
	}
	if strings.Contains(prompt, "username") {
		fmt.Print("x-access-token")
	} else {
		fmt.Print(os.Getenv("GITHUB_TOKEN"))
	}
}

func runCycle(ctx context.Context, client *githubClient, backupDir string) error {
	login, err := client.login(ctx)
	if err != nil {
		return err
	}
	repos, err := client.ownedRepositories(ctx, login)
	if err != nil {
		return err
	}
	log.Printf("discovered %d repositories owned by %s", len(repos), login)

	var failures []error
	for _, repo := range repos {
		if err := backupRepository(ctx, backupDir, repo); err != nil {
			log.Printf("ERROR %s: %v", repo.FullName, err)
			failures = append(failures, fmt.Errorf("%s: %w", repo.FullName, err))
		} else {
			log.Printf("backed up %s", repo.FullName)
		}
	}
	return errors.Join(failures...)
}

func (c *githubClient) login(ctx context.Context) (string, error) {
	var user struct {
		Login string `json:"login"`
	}
	if err := c.getJSON(ctx, "/user", &user); err != nil {
		return "", fmt.Errorf("identify authenticated user: %w", err)
	}
	if user.Login == "" {
		return "", errors.New("GitHub returned an empty authenticated-user login")
	}
	return user.Login, nil
}

func (c *githubClient) ownedRepositories(ctx context.Context, login string) ([]repository, error) {
	var all []repository
	for page := 1; ; page++ {
		path := fmt.Sprintf("/user/repos?affiliation=owner&visibility=all&per_page=100&page=%d", page)
		var batch []repository
		if err := c.getJSON(ctx, path, &batch); err != nil {
			return nil, fmt.Errorf("list repositories (page %d): %w", page, err)
		}
		for _, repo := range batch {
			if strings.EqualFold(repo.Owner.Login, login) {
				all = append(all, repo)
			}
		}
		if len(batch) < 100 {
			return all, nil
		}
	}
}

func (c *githubClient) getJSON(ctx context.Context, path string, dst any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.baseURL+path, nil)
	if err != nil {
		return err
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("Authorization", "Bearer "+c.token)
	req.Header.Set("X-GitHub-Api-Version", apiVersion)
	req.Header.Set("User-Agent", "github-backup")
	resp, err := c.http.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return fmt.Errorf("GitHub API returned %s: %s", resp.Status, strings.TrimSpace(string(body)))
	}
	if err := json.NewDecoder(resp.Body).Decode(dst); err != nil {
		return fmt.Errorf("decode GitHub response: %w", err)
	}
	return nil
}

func backupRepository(ctx context.Context, root string, repo repository) error {
	if repo.FullName == "" || repo.CloneURL == "" {
		return errors.New("GitHub response omitted repository name or clone URL")
	}
	owner, name, ok := strings.Cut(repo.FullName, "/")
	if !ok || !safeName(owner) || !safeName(name) {
		return fmt.Errorf("unsafe repository name %q", repo.FullName)
	}
	ownerDir := filepath.Join(root, owner)
	destination := filepath.Join(ownerDir, name+".git")
	if err := os.MkdirAll(ownerDir, 0o700); err != nil {
		return fmt.Errorf("create owner directory: %w", err)
	}

	info, err := os.Stat(destination)
	if errors.Is(err, os.ErrNotExist) {
		tmp, err := os.MkdirTemp(ownerDir, "."+name+".git.tmp-")
		if err != nil {
			return fmt.Errorf("create temporary clone directory: %w", err)
		}
		if err := os.Remove(tmp); err != nil {
			return fmt.Errorf("prepare temporary clone path: %w", err)
		}
		defer os.RemoveAll(tmp)
		if err := runGit(ctx, "", "clone", "--mirror", repo.CloneURL, tmp); err != nil {
			return err
		}
		if err := configureMirror(ctx, tmp); err != nil {
			return err
		}
		if err := os.Rename(tmp, destination); err != nil {
			return fmt.Errorf("install completed mirror: %w", err)
		}
		return nil
	}
	if err != nil {
		return fmt.Errorf("inspect destination: %w", err)
	}
	if !info.IsDir() {
		return errors.New("destination exists but is not a directory")
	}
	if err := runGit(ctx, destination, "rev-parse", "--is-bare-repository"); err != nil {
		return fmt.Errorf("destination is not a valid bare Git repository: %w", err)
	}
	if err := runGit(ctx, destination, "remote", "set-url", "origin", repo.CloneURL); err != nil {
		return err
	}
	if err := configureMirror(ctx, destination); err != nil {
		return err
	}
	return runGit(ctx, destination, "remote", "update", "--prune")
}

func configureMirror(ctx context.Context, dir string) error {
	settings := [][2]string{
		{"core.logAllRefUpdates", "always"},
		{"gc.reflogExpire", "never"},
		{"gc.reflogExpireUnreachable", "never"},
		{"gc.pruneExpire", "never"},
	}
	for _, setting := range settings {
		if err := runGit(ctx, dir, "config", setting[0], setting[1]); err != nil {
			return err
		}
	}
	return nil
}

func safeName(s string) bool {
	return s != "" && s != "." && s != ".." && !strings.ContainsAny(s, `/\\`)
}

func runGit(ctx context.Context, dir string, args ...string) error {
	self, err := os.Executable()
	if err != nil {
		return fmt.Errorf("locate backup executable: %w", err)
	}
	cmd := exec.CommandContext(ctx, "git", args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(),
		"GIT_TERMINAL_PROMPT=0",
		"GIT_ASKPASS="+self,
		"GITHUB_BACKUP_ASKPASS=1",
	)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("git %s: %w", strings.Join(args, " "), err)
	}
	return nil
}
