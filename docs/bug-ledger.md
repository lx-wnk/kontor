# Bug Ledger

| Date | Symptom | Root cause | Layer | Guarding test |
| ---- | ------- | ---------- | ----- | ------------- |
| 2026-09-23 | Agent token totals inflated while several refreshes run at once | `tokenUsageForFile` added a scanned delta without checking that another caller had already advanced the cache entry | Parser (incremental token cache) | `TestTokenUsageForFile_ConcurrentCallersCountAppendOnce` |
