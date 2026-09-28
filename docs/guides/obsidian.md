# Obsidian vault

Point the dashboard at a local [Obsidian](https://obsidian.md) vault, via the
[Local REST API](https://github.com/coddingtonbear/obsidian-local-rest-api) community plugin, from
**Settings → Obsidian**: base URL, vault root, API key, and TLS mode. All four settings apply as
soon as they are saved, and `baseURL`/`vaultRoot`/`apiKey` are a required trio: set all three, or
none. With only one or two set the vault stays off, and the next start **fails** and names the missing keys, rather than
booting with the vault silently disabled — a vault you configured and that quietly does not run is
worse than a refused start. Clearing all three in the panel (the API key field included) turns the
integration back off.

Once configured, **Index now** in the same panel (or `POST /api/obsidian/index`) turns vault notes
into searchable memory pointers — but only once the `obsidian.read`, `obsidian.search`, and
`memory.write` capability grants exist. A fresh install denies the run otherwise:

```bash
kontor grants add obsidian.read --pattern '*' --scope global --mode allow
kontor grants add obsidian.search --pattern '*' --scope global --mode allow
kontor grants add memory.write --pattern '*' --scope global --mode allow
```

(or the same three from **Settings → Grants**). Agents can also reach the vault directly — read,
search, write, and delete a note — through four MCP tools gated the same way; see
[MCP endpoint](mcp.md#scopes) for the scopes and
[Security](security.md#obsidians-tls-trust-model) for the TLS trust model.
