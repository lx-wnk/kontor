import { ref } from 'vue'
import { errorMessage, readErrorMessage } from '@/utils/errorMessage'

// Mirrors pullRequestView / repoSummary in server/internal/api/github/handler.go.
// That handler owns the wire shape; these names must match its json tags.
// "none" covers both "GitHub reports no check runs" and "the lookup itself
// failed" -- the handler collapses them on purpose, so nothing here may try to
// tell them apart. "not_tracked" means the repository is outside the configured
// allow-list, so no check-run lookup was attempted. `total` can exceed
// passed + failed while runs are queued.
export interface GitHubChecks {
  state: 'success' | 'failure' | 'pending' | 'none' | 'not_tracked'
  passed: number
  failed: number
  total: number
  url: string
}

export interface GitHubPullRequest {
  number: number
  title: string
  author: string
  url: string
  draft: boolean
  updatedAt: string
  // Optional: a server older than the check-run field omits the key entirely.
  checks?: GitHubChecks
}

export interface GitHubRepoSummary {
  repo: string
  pullRequests: GitHubPullRequest[]
  error?: string
  mergeable?: boolean
}

// States only what is known. The route maps every Gate.Authorize failure to
// 403 — missing grant, rate limit, unreadable grant store alike — so the
// fallback names no cause, matching useResources' DENIED_FALLBACK.
const DENIED_FALLBACK = 'The GitHub route refused this read (HTTP 403) without giving a reason.'

export function useGitHubSummary() {
  const repos = ref<GitHubRepoSummary[]>([])
  const loading = ref(true)
  const error = ref<string | null>(null)
  const denied = ref<string | null>(null)
  // 503 from the route means github.token/github.repos are unset: the request
  // was answered, but nothing was ever asked of GitHub. Held apart from
  // `error` because the fix is different — configure it, not repair it.
  const unconfigured = ref(false)

  async function fetchSummary(): Promise<void> {
    loading.value = true
    error.value = null
    denied.value = null
    unconfigured.value = false
    try {
      const res = await fetch('/api/github/summary')
      if (res.status === 503) {
        unconfigured.value = true
        repos.value = []
        return
      }
      if (res.status === 403) {
        denied.value = await readErrorMessage(res, DENIED_FALLBACK)
        repos.value = []
        return
      }
      if (!res.ok)
        throw new Error(await readErrorMessage(res, `Failed to load the GitHub summary (HTTP ${res.status})`))
      const body = await res.json() as { repos?: GitHubRepoSummary[] }
      repos.value = body.repos ?? []
    }
    catch (e) {
      // Cleared on failure: leaving the previous answer on screen under a
      // failure notice would misreport what GitHub holds now.
      repos.value = []
      error.value = errorMessage(e, 'Failed to load the GitHub summary')
    }
    finally {
      loading.value = false
    }
  }

  return { repos, loading, error, denied, unconfigured, fetchSummary }
}

/**
 * Thrown by mergePullRequest so the panel can tell a refusal apart from a
 * GitHub-side failure: 403 is this server refusing (the repository is not in
 * `github.repos`, or no `github.merge` grant exists), anything else came back
 * from GitHub — a conflict, branch protection, an unmergeable head.
 */
export class MergeRefused extends Error {
  status: number
  constructor(message: string, status: number) {
    super(message)
    this.name = 'MergeRefused'
    this.status = status
  }
}

export async function mergePullRequest(repo: string, number: number): Promise<string> {
  const res = await fetch('/api/github/merge', {
    method: 'POST',
    headers: { 'Content-Type': 'application/json', 'Origin': window.location.origin },
    body: JSON.stringify({ repo, number }),
  })
  if (!res.ok)
    throw new MergeRefused(await readErrorMessage(res, `Merge failed (HTTP ${res.status})`), res.status)
  const body = await res.json() as { sha?: string }
  return body.sha ?? ''
}
