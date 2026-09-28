# Bug Ledger

| Date | Symptom | Root cause | Layer | Guarding test |
| ---- | ------- | ---------- | ----- | ------------- |
| 2026-09-23 | Agent token totals inflated while several refreshes run at once | `tokenUsageForFile` added a scanned delta without checking that another caller had already advanced the cache entry | Parser (incremental token cache) | `TestTokenUsageForFile_ConcurrentCallersCountAppendOnce` |
| 2026-09-23 | Desktop app opened from Finder never serves a page | `claudeconfig.Watch` added `$HOME` to an fsnotify kqueue watch, which opens every entry, and `open(~/Desktop)` waits on the TCC prompt | Server startup (config watcher) | none: TCC cannot be staged in a test; verified by `open bin/Kontor.app` reaching 200 in 1s |
| 2026-09-25 | curlNoteTouches emits read edges from heredoc bodies | shellSegmentRe splits on \n so heredoc body lines become their own segments and vaultURLRe matches any /vault/ mention in them | Parser (curlNoteTouches) | TestCurlNoteTouches/heredoc_body_vault_URL_is_not_a_touch |
| 2026-09-25 | expandShellAssignments treats curl -d key=value as a shell assignment, polluting the var table and producing false note touches via $key substitution in later segments | shellAssignRe prefix matched any whitespace, not only command-position separators | Parser (expandShellAssignments) | TestCurlNoteTouches/curl_-d_arg_does_not_pollute_var_table |
