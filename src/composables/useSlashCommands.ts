import { bulkResolvePermissionRequests } from '@/features/pipeline'
import { SLUG_RE } from '../utils/validation'

interface ApiError { error?: string }

export interface SlashCommandDef {
  name: string
  description: string
  usage?: string
  requiresTask?: boolean
}

export interface CommandResult {
  ok: boolean
  message: string
  loading?: boolean
}

export const SLASH_COMMAND_DEFS: SlashCommandDef[] = [
  { name: '/spawn', description: 'Create new pipeline task', usage: '<slug> <description>' },
  { name: '/grant', description: 'Grant open permission request', usage: '<toolName>', requiresTask: true },
  { name: '/cancel', description: 'Cancel current task', requiresTask: true },
  { name: '/retry', description: 'Restart failed stage', requiresTask: true },
  { name: '/promote', description: 'Advance task to next stage (skip approval gate)', requiresTask: true },
  { name: '/help', description: 'List all available commands' },
]

/**
 * Parse a slash command string into [command, args].
 * Supports basic quoted arguments, e.g.: /spawn my-slug "My Task Description"
 * Note: nested quotes are not supported.
 */
export function parseSlashCommand(raw: string): [string, string[]] | null {
  const trimmed = raw.trim()
  if (!trimmed.startsWith('/'))
    return null

  const tokens: string[] = []
  let current = ''
  let inQuote = false

  for (let i = 0; i < trimmed.length; i++) {
    const ch = trimmed[i]
    if (ch === '"') {
      inQuote = !inQuote
    }
    else if (ch === ' ' && !inQuote) {
      if (current.length > 0) {
        tokens.push(current)
        current = ''
      }
    }
    else {
      current += ch
    }
  }
  if (current.length > 0)
    tokens.push(current)
  if (tokens.length === 0)
    return null

  const [cmd, ...args] = tokens
  return [cmd.toLowerCase(), args]
}

export interface DispatchContext {
  taskId?: string
  cwd?: string
}

export async function dispatchSlashCommand(
  cmd: string,
  args: string[],
  ctx: DispatchContext,
): Promise<CommandResult> {
  switch (cmd) {
    case '/help':
      return {
        ok: true,
        message: SLASH_COMMAND_DEFS
          .map(d => `${d.name}${d.usage ? ` ${d.usage}` : ''} — ${d.description}`)
          .join('\n'),
      }

    case '/spawn': {
      const [slug, ...descParts] = args
      const description = descParts.join(' ')
      if (!slug || !description)
        return { ok: false, message: 'Usage: /spawn <slug> <description>' }
      if (!SLUG_RE.test(slug))
        return { ok: false, message: `Invalid slug "${slug}". Format: [a-z0-9][a-z0-9-]{0,63}` }
      if (!ctx.cwd)
        return { ok: false, message: '/spawn requires a working directory. Open an agent with a known cwd.' }
      try {
        const res = await fetch('/api/tasks', {
          method: 'POST',
          headers: { 'Content-Type': 'application/json' },
          body: JSON.stringify({ slug, title: description, cwd: ctx.cwd }),
        })
        if (!res.ok) {
          const data = await res.json().catch(() => ({})) as ApiError
          return { ok: false, message: data.error ?? `Error ${res.status}` }
        }
        return { ok: true, message: `Task "${slug}" created.` }
      }
      catch {
        return { ok: false, message: 'Network error creating task.' }
      }
    }

    case '/grant': {
      if (!ctx.taskId)
        return { ok: false, message: '/grant requires a linked pipeline task.' }
      const [toolName] = args
      if (!toolName)
        return { ok: false, message: 'Usage: /grant <toolName>' }
      try {
        // Step 1: load pending permission requests
        const listRes = await fetch(`/api/tasks/${ctx.taskId}/permission-requests`)
        if (!listRes.ok)
          return { ok: false, message: `Could not load permission requests (${listRes.status}).` }
        const requests = await listRes.json() as Array<{ id: string, tool: string }>
        const match = requests.find(r => r.tool.toLowerCase() === toolName.toLowerCase())
        if (!match)
          return { ok: false, message: `No open permission request found for tool "${toolName}".` }

        // Step 2: resolve it. The single-request route this used to post to
        // is not registered, so the command has never worked; bulk-resolve is
        // the one the triage band uses and it takes the same four decisions.
        await bulkResolvePermissionRequests(ctx.taskId, [match.id], 'allow_once')
        return { ok: true, message: `Permission granted for "${toolName}".` }
      }
      catch {
        return { ok: false, message: 'Network error granting permission.' }
      }
    }

    case '/cancel': {
      if (!ctx.taskId)
        return { ok: false, message: '/cancel requires a linked pipeline task.' }
      try {
        const res = await fetch(`/api/tasks/${ctx.taskId}/cancel`, { method: 'POST' })
        if (!res.ok) {
          const data = await res.json().catch(() => ({})) as ApiError
          return { ok: false, message: data.error ?? `Error ${res.status}` }
        }
        return { ok: true, message: 'Task cancelled.' }
      }
      catch {
        return { ok: false, message: 'Network error cancelling task.' }
      }
    }

    case '/retry': {
      if (!ctx.taskId)
        return { ok: false, message: '/retry requires a linked pipeline task.' }
      try {
        const res = await fetch(`/api/tasks/${ctx.taskId}/retry`, { method: 'POST' })
        if (!res.ok) {
          const data = await res.json().catch(() => ({})) as ApiError
          return { ok: false, message: data.error ?? `Error ${res.status}` }
        }
        return { ok: true, message: 'Stage restarted.' }
      }
      catch {
        return { ok: false, message: 'Network error retrying stage.' }
      }
    }

    case '/promote': {
      if (!ctx.taskId)
        return { ok: false, message: '/promote requires a linked pipeline task.' }
      try {
        const res = await fetch(`/api/tasks/${ctx.taskId}/progress`, { method: 'POST' })
        if (!res.ok) {
          const data = await res.json().catch(() => ({})) as ApiError
          return { ok: false, message: data.error ?? `Error ${res.status}` }
        }
        return { ok: true, message: 'Task promoted to next stage.' }
      }
      catch {
        return { ok: false, message: 'Network error promoting task.' }
      }
    }

    default:
      return { ok: false, message: `Unknown command "${cmd}". Type /help for an overview.` }
  }
}

interface DynamicCommand {
  name: string
  description: string
  source: string
  /** `argument-hint:` frontmatter, e.g. "[base-branch] [--apply-fixes]". */
  argumentHint?: string
}

interface DynamicCommandsResponse {
  commands: DynamicCommand[]
  engineVersion?: string
  builtinsMayBeStale?: boolean
  scopeSource?: string
  scopeLabel?: string
}

/**
 * Scope for resolving a session's slash commands. Prefer sessionId for a live
 * agent (resolves that session's actual CLAUDE_CONFIG_DIR); spawnerId previews
 * a spawner's set; cwd adds project-local <cwd>/.claude/commands.
 */
export interface DynamicCommandScope {
  sessionId?: string
  spawnerId?: string
  cwd?: string
}

/**
 * A session's commands plus how much to trust the list. The built-in commands
 * are curated per Claude Code version, so a session running a different version
 * may know commands the dashboard cannot list — `builtinsMayBeStale` says so.
 */
export interface DynamicCommandSet {
  commands: SlashCommandDef[]
  builtinsMayBeStale: boolean
  engineVersion?: string
}

// A fresh object per call: callers assign `.commands` straight into a ref, so a
// shared instance would let one component's mutation reach every other.
export function emptyCommandSet(): DynamicCommandSet {
  return { commands: [], builtinsMayBeStale: false }
}

// A command installed mid-session is invisible until its scope's entry expires,
// so the lifetime is the whole invalidation. 60 s matches the two caches next to
// it (useObsidianGraph, the server's graph cache).
const DYNAMIC_COMMAND_TTL_MS = 60_000

const dynamicCommandCache = new Map<string, { at: number, set: DynamicCommandSet }>()

/** Entries currently held. Exported so a test (or devtools) can see the map stay bounded. */
export function dynamicCommandCacheSize(): number {
  return dynamicCommandCache.size
}

function cacheSet(key: string, set: DynamicCommandSet, now: number): void {
  for (const [k, entry] of dynamicCommandCache) {
    if (now - entry.at >= DYNAMIC_COMMAND_TTL_MS)
      dynamicCommandCache.delete(k)
  }
  dynamicCommandCache.set(key, { at: now, set })
}

function scopeKey(scope: DynamicCommandScope): string {
  // Prefix per identifier kind so distinct namespaces (a session id, a spawner
  // id, a cwd) can never collide in the cache.
  if (scope.sessionId)
    return `sess:${scope.sessionId}`
  if (scope.spawnerId)
    return `spawner:${scope.spawnerId}`
  if (scope.cwd)
    return `cwd:${scope.cwd}`
  return 'default'
}

export async function fetchDynamicCommands(scope: DynamicCommandScope): Promise<DynamicCommandSet> {
  const key = scopeKey(scope)
  const now = Date.now()
  const hit = dynamicCommandCache.get(key)
  if (hit && now - hit.at < DYNAMIC_COMMAND_TTL_MS)
    return hit.set

  const params = new URLSearchParams()
  if (scope.sessionId)
    params.set('sessionId', scope.sessionId)
  if (scope.spawnerId)
    params.set('spawnerId', scope.spawnerId)
  if (scope.cwd)
    params.set('cwd', scope.cwd)

  try {
    const res = await fetch(`/api/slash-commands?${params.toString()}`)
    if (!res.ok)
      return emptyCommandSet()
    const data = await res.json() as DynamicCommandsResponse
    const set: DynamicCommandSet = {
      commands: (data.commands ?? []).map(c => ({
        name: c.name,
        description: c.description,
        usage: c.argumentHint || undefined,
      })),
      builtinsMayBeStale: !!data.builtinsMayBeStale,
      engineVersion: data.engineVersion,
    }
    cacheSet(key, set, now)
    return set
  }
  catch {
    return emptyCommandSet()
  }
}
