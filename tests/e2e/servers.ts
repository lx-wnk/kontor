/**
 * The suite runs against two servers. The app server carries every spec and
 * runs with the per-IP limiter raised, because the suite drives more requests
 * per second than the production rate allows and the resulting 429s would hide
 * real ones. The limiter server keeps the production default, so the one spec
 * that is about the limiter still measures the real control.
 */
export const APP_PORT = 13199
export const LIMITER_PORT = 13198

export const APP_BASE_URL = `http://localhost:${APP_PORT}`
export const LIMITER_BASE_URL = `http://localhost:${LIMITER_PORT}`

/** Well above the ~13 requests/s the suite drives; the production default is 10. */
export const APP_RATE_LIMIT_RPS = '1000'
