import { Buffer } from 'node:buffer'
import { execFileSync } from 'node:child_process'
import { randomBytes, timingSafeEqual } from 'node:crypto'
import { mkdirSync, unlinkSync, writeFileSync } from 'node:fs'
import { createServer } from 'node:http'
import { homedir } from 'node:os'
import { join } from 'node:path'
import process from 'node:process'
/**
 * Dashboard Channel MCP Server
 *
 * Spawned by Claude Code as a subprocess via:
 *   claude --dangerously-load-development-channels server:dashboard-channel
 *
 * Architecture:
 *   1. MCP Server on stdio ← communicates with Claude Code
 *   2. HTTP Server on localhost (random port) ← receives messages from dashboard
 *   3. Discovery file at ~/.claude/dashboard-channel/{parentPid}.json
 */
import { Server } from '@modelcontextprotocol/sdk/server/index.js'
import { StdioServerTransport } from '@modelcontextprotocol/sdk/server/stdio.js'
import {
  CallToolRequestSchema,
  ListToolsRequestSchema,
} from '@modelcontextprotocol/sdk/types.js'

const PS_LINE_RE = /^\s*(\d+)\s+(\S.*)$/

/**
 * Walk up the process tree to find the `claude` CLI process PID.
 * Claude Code spawns MCP servers via an intermediate `node` wrapper,
 * so process.ppid is the wrapper — not the claude PID that `ps` reports.
 */
function findClaudePid(): number {
  let pid = process.ppid
  try {
    for (let i = 0; i < 5; i++) {
      const out = execFileSync('ps', ['-o', 'ppid=,comm=', '-p', String(pid)], { encoding: 'utf-8' }).trim()
      const match = out.match(PS_LINE_RE)
      if (!match)
        break
      const comm = match[2]
      if (comm.endsWith('/claude') || comm === 'claude')
        return pid
      pid = Number.parseInt(match[1], 10)
      if (pid <= 1)
        break
    }
  }
  catch { /* fallback to direct parent */ }
  return process.ppid
}

// ─── Config ──────────────────────────────────────────────
const DASHBOARD_PORT = Number.parseInt(process.env.DASHBOARD_PORT || '13120', 10)
const DISCOVERY_DIR = join(homedir(), '.claude', 'dashboard-channel')
const PARENT_PID = findClaudePid()
const TOKEN = randomBytes(16).toString('hex')

let discoveryPath: string | null = null

// ─── MCP Server ──────────────────────────────────────────

const CHANNEL_INSTRUCTIONS = `
Messages from the monitoring dashboard arrive as <channel source="dashboard" type="instruction">.
These are instructions or questions from the user watching your activity in a separate browser UI.

## How to handle dashboard messages

1. FINISH your current action (tool call, edit, etc.) before addressing the message.
   Never abandon a half-finished operation — complete it cleanly, then handle the instruction.

2. ACKNOWLEDGE immediately using the dashboard_reply tool with a short status:
   "Understood, will [action] after current step." (keep under 100 chars)

3. ASSESS priority yourself. The message may be:
   - A course correction ("stop working on X, focus on Y instead")
   - A supplementary hint ("also consider Z")
   - A question ("what is the status of X?")
   - A directive ("run the tests now")
   Use your judgment to decide whether to interrupt your plan or weave it in.

4. REPORT progress after each significant step (file edit, test run, tool completion)
   using dashboard_reply. Keep updates short and technical:
   "Edited auth.ts — running tests next."
   "Tests passed (14/14). Moving to refactor."

5. REPORT completion with a final dashboard_reply summarizing what you did:
   "Done: refactored auth module, 3 files changed, tests green."

## Reply format rules
- Use dashboard_reply for ALL communication back to the dashboard.
- Keep replies under 200 chars. No markdown, no code blocks, just plain status text.
- Never skip the initial acknowledgment — the user needs to see their message arrived.
`.trim()

const mcp = new Server(
  { name: 'dashboard-channel', version: '0.1.0' },
  {
    capabilities: {
      experimental: { 'claude/channel': {} },
      tools: {},
    },
    instructions: CHANNEL_INSTRUCTIONS,
  },
)

// ─── Reply Tool ──────────────────────────────────────────
// Claude calls this to send a message back to the dashboard

mcp.setRequestHandler(ListToolsRequestSchema, async () => ({
  tools: [
    {
      name: 'dashboard_reply',
      description:
        'Send a reply back to the monitoring dashboard. Use this when you have completed an instruction from the dashboard or want to report status/progress.',
      inputSchema: {
        type: 'object' as const,
        properties: {
          message: {
            type: 'string',
            description: 'The reply message to display in the dashboard',
          },
        },
        required: ['message'],
      },
    },
    {
      name: 'request_permission',
      description:
        'Request one or more tool permissions. CRITICAL: every missed permission triggers a kill-and-restart of this stage agent — request piecemeal and you will burn through the task in restart loops. Forward-scan the ENTIRE remaining plan before each call.\n\nMandatory pattern (do NOT deviate):\n  1. As your FIRST action in a task, walk through the plan step-by-step and enumerate every distinct tool you will touch — Bash patterns (e.g. `pnpm test*`, `pnpm lint*`, `pnpm typecheck*`, `git add*`, `git commit*`, `git diff*`, `git status*`, `git checkout*`), WebFetch URLs, Writes outside the worktree, etc.\n  2. Call this tool ONCE with the full `permissions: [...]` array. Pre-granted entries auto-resolve silently; only genuinely new entries surface as ON HOLD.\n  3. If you receive a `[PERMISSION GRANTED]` resume note mid-task, treat it as a signal that you under-enumerated — re-scan the rest of the work and bulk-request EVERYTHING else you anticipate, then continue.\n\nThe legacy single-tool form (`tool` + `pattern`) is kept for compatibility but is strongly discouraged — every single-tool request that lands here counts as a re-request cycle and is logged.',
      inputSchema: {
        type: 'object' as const,
        properties: {
          stageRunId: {
            type: 'string',
            description: 'The current stage_run id (auto-injected from DASHBOARD_STAGE_RUN_ID env)',
          },
          permissions: {
            type: 'array',
            description: 'Bulk request — preferred. Each item: {tool, pattern?, reason?}. Pre-granted entries auto-resolve.',
            items: {
              type: 'object',
              properties: {
                tool: { type: 'string', description: 'Tool name (e.g. Bash, WebFetch, Write)' },
                pattern: { type: 'string', description: 'Optional pattern (e.g. "git push *"); omit for full tool' },
                reason: { type: 'string', description: 'Why this tool is needed (per-entry, optional)' },
              },
              required: ['tool'],
            },
          },
          tool: {
            type: 'string',
            description: 'Single-tool legacy form. Use `permissions[]` instead when possible.',
          },
          pattern: {
            type: 'string',
            description: 'Single-tool legacy form pattern.',
          },
          reason: {
            type: 'string',
            description: 'Top-level reason; used as fallback per-entry reason and shown in ON HOLD notifications.',
          },
        },
        // Either `permissions` OR `tool` must be present — enforced at runtime.
      },
    },
    {
      name: 'set_stage_output',
      description:
        'Submit this stage\'s structured result to the dashboard. Call this as your FINAL action — the dashboard validates the object against the per-stage schema and returns an error on mismatch; you must fix the output and call again. This is the reliable replacement for emitting a ```json block in a message.',
      inputSchema: {
        type: 'object' as const,
        properties: {
          output: {
            type: 'object',
            description: 'The stage result object, in the exact shape your prompt specified.',
          },
          stageRunId: {
            type: 'string',
            description: 'auto-injected from DASHBOARD_STAGE_RUN_ID env',
          },
        },
        required: ['output'],
      },
    },
  ],
}))

mcp.setRequestHandler(CallToolRequestSchema, async (req) => {
  if (req.params.name === 'dashboard_reply') {
    const args = req.params.arguments as { message?: string } | undefined
    const message = args?.message
    if (typeof message !== 'string' || !message) {
      return {
        content: [{ type: 'text', text: 'dashboard_reply requires a `message` string.' }],
        isError: true,
      }
    }

    try {
      const res = await fetch(`http://127.0.0.1:${DASHBOARD_PORT}/api/channel-reply`, {
        method: 'POST',
        headers: {
          'Content-Type': 'application/json',
          'Authorization': `Bearer ${TOKEN}`,
        },
        body: JSON.stringify({
          parentPid: PARENT_PID,
          message,
          timestamp: new Date().toISOString(),
        }),
      })

      if (!res.ok) {
        return { content: [{ type: 'text', text: `Dashboard reply failed: ${res.status}` }] }
      }

      return { content: [{ type: 'text', text: 'Reply sent to dashboard.' }] }
    }
    catch (err) {
      return {
        content: [{ type: 'text', text: `Could not reach dashboard: ${(err as Error).message}` }],
      }
    }
  }

  if (req.params.name === 'request_permission') {
    const args = req.params.arguments as {
      stageRunId?: string
      permissions?: Array<{ tool: string, pattern?: string, reason?: string }>
      tool?: string
      pattern?: string
      reason?: string
    }
    const stageRunId = args.stageRunId || process.env.DASHBOARD_STAGE_RUN_ID
    if (!stageRunId) {
      return {
        content: [
          { type: 'text', text: 'No stageRunId provided — cannot request permission. Task is not orchestrator-managed.' },
        ],
      }
    }

    // Normalize to bulk shape. Either `permissions[]` (preferred) or
    // single-tool legacy form is accepted.
    const entries = Array.isArray(args.permissions) && args.permissions.length > 0
      ? args.permissions.map(p => ({
          tool: p.tool,
          pattern: p.pattern ?? null,
          reason: p.reason ?? args.reason ?? null,
        }))
      : args.tool
        ? [{ tool: args.tool, pattern: args.pattern ?? null, reason: args.reason ?? null }]
        : null

    if (!entries) {
      return {
        content: [{ type: 'text', text: 'request_permission needs either `permissions: [...]` or single-tool `tool`+`reason`.' }],
      }
    }

    try {
      const res = await fetch(`http://127.0.0.1:${DASHBOARD_PORT}/api/permission-requests/bulk`, {
        method: 'POST',
        headers: {
          'Content-Type': 'application/json',
          'Authorization': `Bearer ${TOKEN}`,
        },
        body: JSON.stringify({ stageRunId, entries }),
      })
      if (!res.ok) {
        // Graceful fallback ONLY when the dashboard does not implement the
        // bulk endpoint (HTTP 404). Any other non-OK status (4xx validation,
        // expired token, 5xx) is a real error — falling back to N legacy
        // single-tool requests would silently inflate the re-request cycle
        // counter and look like a permission-loop in telemetry.
        if (res.status === 404) {
          const fallbackResults: string[] = []
          for (const e of entries) {
            const single = await fetch(`http://127.0.0.1:${DASHBOARD_PORT}/api/permission-requests`, {
              method: 'POST',
              headers: { 'Content-Type': 'application/json', 'Authorization': `Bearer ${TOKEN}` },
              body: JSON.stringify({ stageRunId, tool: e.tool, pattern: e.pattern, reason: e.reason }),
            })
            fallbackResults.push(`${e.tool}${e.pattern ? `(${e.pattern})` : ''}: ${single.status}`)
          }
          return {
            content: [{ type: 'text', text: `Bulk endpoint returned 404; fell back to single-request: ${fallbackResults.join('; ')}` }],
          }
        }
        const errBody = await res.text().catch(() => '')
        return {
          content: [{ type: 'text', text: `Bulk endpoint returned ${res.status}: ${errBody.slice(0, 500)}` }],
          isError: true,
        }
      }
      const body = (await res.json()) as {
        autoResolved: Array<{ tool: string, pattern: string | null }>
        pending: Array<{ id: string, tool: string, pattern: string | null }>
        cycleCount?: number
        loopWarning?: string | null
      }
      const autoMsg = body.autoResolved.length > 0
        ? `${body.autoResolved.length} auto-resolved (already granted): ${body.autoResolved.map(e => e.tool + (e.pattern ? `(${e.pattern})` : '')).join(', ')}`
        : 'no auto-resolves'
      const pendMsg = body.pending.length > 0
        ? `${body.pending.length} ON HOLD awaiting user: ${body.pending.map(e => e.tool + (e.pattern ? `(${e.pattern})` : '')).join(', ')}`
        : 'all granted — proceed'
      const loopMsg = body.loopWarning ? `\n\n⚠️ ${body.loopWarning}` : ''
      return {
        content: [{ type: 'text', text: `${autoMsg}. ${pendMsg}.${loopMsg}` }],
      }
    }
    catch (err) {
      return {
        content: [{ type: 'text', text: `Could not reach dashboard: ${(err as Error).message}` }],
      }
    }
  }

  if (req.params.name === 'set_stage_output') {
    const args = req.params.arguments as { output?: Record<string, unknown>, stageRunId?: string }
    const stageRunId = args.stageRunId || process.env.DASHBOARD_STAGE_RUN_ID
    if (!stageRunId) {
      return {
        content: [{ type: 'text', text: 'No stageRunId — cannot submit stage output. Task is not orchestrator-managed.' }],
        isError: true,
      }
    }
    if (!args.output || typeof args.output !== 'object') {
      return {
        content: [{ type: 'text', text: 'set_stage_output requires an `output` object.' }],
        isError: true,
      }
    }
    try {
      const res = await fetch(`http://127.0.0.1:${DASHBOARD_PORT}/api/channel-stage-output`, {
        method: 'POST',
        headers: {
          'Content-Type': 'application/json',
          'Authorization': `Bearer ${TOKEN}`,
        },
        body: JSON.stringify({ stageRunId, output: args.output }),
      })
      if (!res.ok) {
        const errBody = await res.text().catch(() => '')
        return {
          content: [{ type: 'text', text: `Stage output rejected (${res.status}): ${errBody.slice(0, 500)}. Fix and call set_stage_output again.` }],
          isError: true,
        }
      }
      return { content: [{ type: 'text', text: 'Stage output accepted.' }] }
    }
    catch (err) {
      return {
        content: [{ type: 'text', text: `Could not reach dashboard: ${(err as Error).message}` }],
        isError: true,
      }
    }
  }

  return { content: [{ type: 'text', text: `Unknown tool: ${req.params.name}` }] }
})

// ─── HTTP Server (receives messages from dashboard) ──────

const MAX_BODY_BYTES = 64 * 1024

class PayloadTooLargeError extends Error {}

function readBody(req: import('node:http').IncomingMessage): Promise<string> {
  return new Promise((resolve, reject) => {
    const chunks: Buffer[] = []
    let size = 0
    req.on('data', (c: Buffer) => {
      size += c.length
      if (size > MAX_BODY_BYTES) {
        req.destroy()
        reject(new PayloadTooLargeError(`Request body exceeds ${MAX_BODY_BYTES} byte limit`))
        return
      }
      chunks.push(c)
    })
    req.on('end', () => resolve(Buffer.concat(chunks).toString()))
    req.on('error', reject)
  })
}

const ALLOWED_ORIGINS = [
  `http://localhost:${DASHBOARD_PORT}`,
  `http://127.0.0.1:${DASHBOARD_PORT}`,
]

function getAllowedOrigin(req: import('node:http').IncomingMessage): string {
  const origin = req.headers.origin || ''
  if (ALLOWED_ORIGINS.includes(origin))
    return origin
  return ALLOWED_ORIGINS[0]
}

// Constant-time Bearer check against the per-process discovery token.
// The dashboard is the only caller and always sends this header.
function isAuthorized(req: import('node:http').IncomingMessage): boolean {
  const header = req.headers.authorization || ''
  const expected = `Bearer ${TOKEN}`
  const a = Buffer.from(header)
  const b = Buffer.from(expected)
  return a.length === b.length && timingSafeEqual(a, b)
}

const httpServer = createServer(async (req, res) => {
  // CORS preflight
  if (req.method === 'OPTIONS') {
    res.writeHead(204, {
      'Access-Control-Allow-Origin': getAllowedOrigin(req),
      'Access-Control-Allow-Methods': 'POST, GET',
      'Access-Control-Allow-Headers': 'Content-Type',
    })
    res.end()
    return
  }

  const url = new URL(req.url || '/', `http://127.0.0.1`)

  // Health check
  if (req.method === 'GET' && url.pathname === '/health') {
    res.writeHead(200, { 'Content-Type': 'application/json' })
    res.end(JSON.stringify({ status: 'ok', parentPid: PARENT_PID }))
    return
  }

  // Receive message from dashboard → forward to Claude
  if (req.method === 'POST' && url.pathname === '/message') {
    if (!isAuthorized(req)) {
      res.writeHead(401, { 'Content-Type': 'application/json' })
      res.end(JSON.stringify({ error: 'Unauthorized' }))
      return
    }
    try {
      const body = JSON.parse(await readBody(req))
      const message = body.message as string

      if (!message || typeof message !== 'string') {
        res.writeHead(400, { 'Content-Type': 'application/json' })
        res.end(JSON.stringify({ error: 'Missing "message" field' }))
        return
      }

      await mcp.notification({
        method: 'notifications/claude/channel',
        params: {
          content: message,
          meta: { source: 'dashboard', type: 'instruction' },
        },
      })

      res.writeHead(200, { 'Content-Type': 'application/json' })
      res.end(JSON.stringify({ ok: true }))
    }
    catch (err) {
      if (err instanceof PayloadTooLargeError) {
        res.writeHead(413, { 'Content-Type': 'application/json' })
        res.end(JSON.stringify({ error: err.message }))
        return
      }
      res.writeHead(500, { 'Content-Type': 'application/json' })
      res.end(JSON.stringify({ error: (err as Error).message }))
    }
    return
  }

  res.writeHead(404)
  res.end('Not found')
})

// ─── Discovery File ──────────────────────────────────────

function writeDiscovery(port: number) {
  mkdirSync(DISCOVERY_DIR, { recursive: true })
  discoveryPath = join(DISCOVERY_DIR, `${PARENT_PID}.json`)
  writeFileSync(
    discoveryPath,
    JSON.stringify({
      port,
      channelPid: process.pid,
      parentPid: PARENT_PID,
      cwd: process.cwd(),
      token: TOKEN,
      startedAt: new Date().toISOString(),
    }),
    { mode: 0o600 },
  )
}

function cleanup() {
  if (discoveryPath) {
    try {
      unlinkSync(discoveryPath)
    }
    catch {
      // File may already be gone
    }
  }
}

process.on('SIGTERM', () => {
  cleanup()
  process.exit(0)
})
process.on('SIGINT', () => {
  cleanup()
  process.exit(0)
})
process.on('exit', cleanup)

// ─── Startup ─────────────────────────────────────────────

httpServer.listen(0, '127.0.0.1', async () => {
  const addr = httpServer.address()
  const port = typeof addr === 'object' && addr ? addr.port : 0

  writeDiscovery(port)

  // Connect MCP over stdio to Claude Code
  const transport = new StdioServerTransport()
  await mcp.connect(transport)

  // Log to stderr (stdout is reserved for MCP stdio)
  console.error(`[dashboard-channel] HTTP on port ${port}, parent PID ${PARENT_PID}`)
})
