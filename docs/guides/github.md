# GitHub integration

Point the dashboard at GitHub from **Settings → GitHub**: a fine-grained personal access token, a
comma-separated `owner/name` repository allow-list, and a base URL (defaults to
`https://api.github.com`; override it for GitHub Enterprise). All three apply only after a server
restart, and the token and the repository list are a required pair — set both, or clear both. A
half-set pair fails the next start and names the missing key, rather than booting with the
integration silently disabled; the base URL is not part of that pair, since it always carries a
default and so is never a missing half of anything.

**Or no token at all.** Set **Token source** to *From the GitHub CLI* (`github.tokenSource =
gh-cli`) and the dashboard reads the token from `gh auth token` at each start instead of holding
one. The token field disappears, the pair rule reduces to the repository list alone, and nothing is
stored: the credential stays in the GitHub CLI's own store. A `gh` that is missing or logged out
fails the next start and says which, rather than booting into GitHub calls that all answer 401. This
narrows what is *stored*, not what an agent on this machine can *reach* — an agent with Bash runs as
the same OS user and can invoke `gh` itself either way.

Once configured, the **GitHub** tile reads open pull requests from
`GET /api/github/summary`: each allow-listed repository's own open pull requests, merged with
whatever the `involves:@me` search finds you across every repository your token can see, deduped,
sorted by most recently updated, and capped at 20. `GET /api/github/search`, `POST /api/github/comment`, and
`POST /api/github/merge` reach the same allow-listed repositories — every route bounded to
`github.repos` before it is bounded by a capability at all. Each route, and its matching `github_*`
MCP tool, is gated by one of four capabilities. A fresh install denies all four by default:

```bash
kontor grants add github.read --pattern '*' --scope global --mode allow
kontor grants add github.search --pattern '*' --scope global --mode allow
```

`github.comment` and `github.merge` are deliberately not on that list. Posting a comment is public
and irreversible the moment it lands, so it is not something an initial setup script should hand out
sight-unseen — grant it explicitly, the same way, once you actually want an agent to comment.
`github.merge` is class `spend`, so with no grant it is denied outright rather than surfaced as a
prompt, regardless of whether you ever add one to that list — see
[Security](security.md#githubs-token-and-repository-boundary) for why. See
[MCP endpoint](mcp.md#scopes) for the `github:read`/`github:write`/`github:merge` scopes.

**GitHub Enterprise on a LAN address is not reachable today.** The client dials through the same
SSRF guard the rest of the server uses (`validation.SafeDialContext`), which refuses loopback,
private, link-local, and CGNAT addresses at connection time — so a GHE host on `10.x`/`192.168.x`
fails to dial, with no clearer error than a failed connection. Widening the shared guard for one
application was rejected the same way it was for Obsidian's own TLS trust model; the fix, if this is
ever needed, is a narrow per-client dial policy, not a change to `validation.IsBlockedIP`.
