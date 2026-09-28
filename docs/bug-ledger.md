# Bug Ledger

| Date | Symptom | Root cause | Layer | Guarding test |
| ---- | ------- | ---------- | ----- | ------------- |
| 2026-09-23 | Agent token totals inflated while several refreshes run at once | `tokenUsageForFile` added a scanned delta without checking that another caller had already advanced the cache entry | Parser (incremental token cache) | `TestTokenUsageForFile_ConcurrentCallersCountAppendOnce` |
| 2026-09-23 | Desktop app opened from Finder never serves a page | `claudeconfig.Watch` added `$HOME` to an fsnotify kqueue watch, which opens every entry, and `open(~/Desktop)` waits on the TCC prompt | Server startup (config watcher) | none: TCC cannot be staged in a test; verified by `open bin/Kontor.app` reaching 200 in 1s |
