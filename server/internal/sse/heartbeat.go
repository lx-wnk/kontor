package sse

import "time"

// HeartbeatInterval is the longest an SSE stream stays silent; a comment frame
// fills the gap so proxies and client body timeouts (undici: 300s) keep it open.
const HeartbeatInterval = 30 * time.Second

// HeartbeatComment is the text of the keepalive comment frame.
var HeartbeatComment = []byte("heartbeat")
