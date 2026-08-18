# GitHub Backup

`github-backup` discovers every public and private repository owned by the
authenticated personal GitHub account, clones new repositories as bare mirrors,
and updates existing mirrors every 12 hours.

A mirror contains all Git branches, tags, notes, and other refs advertised by
GitHub. Reflogs are enabled and their expiry/pruning is disabled, so previous
object IDs remain recoverable after force-pushes or upstream ref deletion. This
retains history indefinitely and means disk usage can only grow. The tool does
not delete a local mirror when a repository disappears from GitHub.

## Requirements

- Go 1.22 or newer (only needed to build)
- Git
- A GitHub personal access token (see below)

## Build and run

```sh
go build -o github-backup .
mkdir -p "$HOME/github-backups"
GITHUB_TOKEN='github_pat_...' ./github-backup -dir "$HOME/github-backups"
```

The first cycle runs immediately. Leave the process running; subsequent cycles
start every 12 hours. Useful options:

```text
-dir PATH           mirror destination (default ./backups)
-interval DURATION  update frequency (default 12h)
-once               perform one cycle and exit
-api-url URL        API endpoint (for GitHub Enterprise Server)
```

Test token access and the destination with a one-off run:

```sh
GITHUB_TOKEN='github_pat_...' ./github-backup -once -dir "$HOME/github-backups"
```

Avoid putting the token directly in a shell history. A protected environment
file used by a service manager is preferable for long-running use. The token is
used for API calls and Git's ask-pass flow; it is not stored in repository remote
URLs or printed by this program.

## PAT permissions

Preferred: create a **fine-grained personal access token** with your personal
account as the resource owner, select **All repositories** (so repositories
created later are included), and grant repository **Contents: Read-only**.
GitHub automatically includes read-only Metadata permission. No write or admin
permission is needed.

If a fine-grained token cannot cover the repositories you need, use a classic
PAT with the `repo` scope. That scope is broader. If an organization enforces SSO,
the token must also be authorized for that organization; this program currently
backs up repositories owned by the authenticated personal account, not
organization-owned repositories or repositories merely shared with you.

## What is and is not backed up

This protects Git data: commits, branches, tags, notes, and other advertised
refs. It does not back up GitHub-only data such as issues, pull-request comments,
Actions artifacts, releases/assets, repository settings, wikis, or Git LFS
objects. Those require separate backup support.

To inspect or restore a mirror:

```sh
git clone "$HOME/github-backups/OWNER/REPOSITORY.git" restored-copy
```
