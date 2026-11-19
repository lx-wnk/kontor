import { APP_BASE_URL, LIMITER_BASE_URL } from './servers'

/**
 * Each server runs against `.e2e/dashboard-e2e-<port>.db`, which persists between runs.
 * On a fresh file the first-run onboarding dialog is open — it covers the page and
 * swallows every click the specs make. Runs after both webServers are ready, so they are reachable.
 */
export default async function globalSetup(): Promise<void> {
  for (const baseURL of [APP_BASE_URL, LIMITER_BASE_URL])
    await prepare(baseURL)
}

async function prepare(baseURL: string): Promise<void> {
  const res = await fetch(`${baseURL}/api/onboarding/status`, {
    method: 'PATCH',
    // The server rejects mutations whose Origin does not match its own host.
    headers: { 'Content-Type': 'application/json', 'Origin': baseURL },
    body: JSON.stringify({ completed: true }),
  })
  if (!res.ok)
    throw new Error(`global-setup: dismissing onboarding failed on ${baseURL} (${res.status})`)

  // A prior run killed before its afterEach would otherwise leak a stored layout into this one.
  const layoutRes = await fetch(`${baseURL}/api/settings/workspace.layout`, {
    method: 'PATCH',
    headers: { 'Content-Type': 'application/json', 'Origin': baseURL },
    body: JSON.stringify({ value: '' }),
  })
  if (!layoutRes.ok)
    throw new Error(`global-setup: resetting workspace.layout failed on ${baseURL} (${layoutRes.status})`)
}
