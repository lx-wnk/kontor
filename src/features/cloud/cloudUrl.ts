const CLOUD_URL_PREFIX = 'https://claude.ai/code/'

export function isCloudUrl(url: string): boolean {
  return url.startsWith(CLOUD_URL_PREFIX)
}
