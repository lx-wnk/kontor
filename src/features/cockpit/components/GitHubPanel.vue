<script setup lang="ts">
import type { GitHubChecks } from '../composables/useGitHubSummary'
import type { PanelState } from '../panelState'
import { computed, onMounted, ref } from 'vue'
import { mergePullRequest, MergeRefused, useGitHubSummary } from '../composables/useGitHubSummary'
import CockpitPanel from './CockpitPanel.vue'

const { repos, loading, error, denied, unconfigured, fetchSummary } = useGitHubSummary()
onMounted(() => void fetchSummary())

const pullRequests = computed(() => repos.value
  .flatMap(r => r.pullRequests.map(pr => ({ ...pr, repo: r.repo, mergeable: r.mergeable !== false })))
  .sort((a, b) => b.updatedAt.localeCompare(a.updatedAt)))

// The route answers 200 with what it could reach and names what it could not,
// so one rate-limited repository does not blank the others. A repository that
// failed contributes no pull requests, so without this list it is
// indistinguishable from one that simply has none — a failure drawn as an
// empty answer, the exact defect panelState.ts exists to prevent.
const repoFailures = computed(() =>
  repos.value.filter(r => r.error).map(r => `${r.repo}: ${r.error}`),
)

// The order matters and is the whole five-state rule: a refusal is checked
// before an empty answer, so a denied read can never be drawn as "no open
// pull requests", and "not configured" is checked before both, so an
// unconfigured install never looks like a healthy quiet one.
const state = computed<PanelState>(() => {
  if (loading.value)
    return 'loading'
  if (unconfigured.value)
    return 'notAsked'
  if (denied.value)
    return 'denied'
  if (error.value)
    return 'failed'
  if (pullRequests.value.length > 0)
    return 'ready'
  // Nothing came back AND something broke: that is a failure, not an empty
  // answer. Only a clean run with no pull requests is 'empty'.
  return repoFailures.value.length > 0 ? 'failed' : 'empty'
})

// Shape and text carry the state, never hue alone: a red dot and a green dot
// read identically to a colour-blind eye and to a screen reader.
const CHECK_MARKS: Record<GitHubChecks['state'], string> = {
  success: 'OK',
  failure: 'X',
  pending: '...',
  none: '-',
  not_tracked: '○',
}

function checkLabel(checks: GitHubChecks): string {
  switch (checks.state) {
    case 'success':
      return `${checks.passed}/${checks.total}`
    case 'failure':
      return `${checks.failed} failed`
    case 'pending':
      return `${checks.passed + checks.failed}/${checks.total}`
    case 'none':
      return 'no checks'
    case 'not_tracked':
      return 'not tracked'
  }
}

// "none" means the lookup found nothing OR failed -- the handler collapses
// both, so the wording must not promise which one it was.
function checkTitle(checks: GitHubChecks): string {
  if (checks.state === 'none')
    return 'No check runs reported for this pull request'
  if (checks.state === 'not_tracked')
    return 'Checks not tracked: add this repository to github.repos in Settings → GitHub (applies after a server restart)'
  return `Checks ${checks.state}: ${checks.passed} passed, ${checks.failed} failed, ${checks.total} total`
}

// A merge cannot be taken back, so it takes two deliberate clicks and the
// confirmation names the pull request it would merge. `pending` holds the key
// of the row awaiting confirmation, never more than one.
const pending = ref<string | null>(null)
const merging = ref<string | null>(null)
const mergeError = ref('')

const prKey = (pr: { repo: string, number: number }) => `${pr.repo}#${pr.number}`

function askToMerge(pr: { repo: string, number: number }) {
  mergeError.value = ''
  pending.value = prKey(pr)
}

async function confirmMerge(pr: { repo: string, number: number, title: string }) {
  const key = prKey(pr)
  merging.value = key
  mergeError.value = ''
  try {
    await mergePullRequest(pr.repo, pr.number)
    pending.value = null
    await fetchSummary()
  }
  catch (e) {
    // A 403 is this server refusing, and both of its causes are the operator's
    // to fix, so the message names them rather than guessing which one applies.
    mergeError.value = e instanceof MergeRefused && e.status === 403
      ? `${e.message} — check Settings → GitHub for the repository allow-list, and Settings → Grants for a github.merge grant.`
      : e instanceof Error ? e.message : 'Merge failed.'
  }
  finally {
    merging.value = null
  }
}

const message = computed(() => {
  if (unconfigured.value)
    return 'Set github.token and github.repos in Settings → GitHub to switch this on.'
  if (denied.value)
    return denied.value
  return error.value
    ?? (repoFailures.value.length > 0 ? repoFailures.value.join('; ') : undefined)
    ?? 'No open pull request in the configured repositories.'
})
</script>

<template>
  <CockpitPanel id="github" title="GitHub" icon="⌂" :state="state" :message="message">
    <p
      v-if="repoFailures.length > 0"
      data-testid="cockpit-github-partial-failure"
      class="text-[12px] rounded-md px-3 py-2 mb-2 bg-warning-soft text-warning-text"
      role="alert"
    >
      {{ repoFailures.join('; ') }}
    </p>
    <ul class="flex flex-col gap-1.5">
      <li
        v-for="pr in pullRequests.slice(0, 20)"
        :key="`${pr.repo}#${pr.number}`"
        class="flex flex-col gap-0.5 text-[12px] min-w-0"
        :data-testid="`cockpit-github-pr-${pr.number}`"
      >
        <a :href="pr.url" target="_blank" rel="noopener noreferrer" class="truncate min-w-0 flex-1 text-fg hover:text-accent">
          {{ pr.title }}
        </a>
        <div class="flex items-center justify-between gap-2 min-w-0">
          <div class="flex items-center gap-2 min-w-0">
            <a
              :href="pr.url"
              target="_blank"
              rel="noopener noreferrer"
              :data-testid="`cockpit-github-repo-${pr.number}`"
              class="shrink-0 text-fg-mute hover:text-accent"
            >{{ pr.repo }}#{{ pr.number }}</a>
            <span
              v-if="pr.checks"
              :data-testid="`cockpit-github-checks-${pr.number}`"
              class="shrink-0 tabular-nums text-fg-mute"
              :title="checkTitle(pr.checks)"
              :aria-label="checkTitle(pr.checks)"
            >{{ CHECK_MARKS[pr.checks.state] }} {{ checkLabel(pr.checks) }}</span>
          </div>
          <button
            v-if="pr.mergeable && pending !== `${pr.repo}#${pr.number}`"
            type="button"
            :data-testid="`cockpit-github-merge-${pr.number}`"
            class="shrink-0 rounded-md border border-line px-2 py-0.5 text-[11px] text-fg-mute hover:text-fg hover:border-accent"
            @click="askToMerge(pr)"
          >
            Merge
          </button>
          <span v-else-if="pr.mergeable" class="min-w-0 flex items-center gap-1.5">
            <span :data-testid="`cockpit-github-merge-confirm-text-${pr.number}`" class="min-w-0 truncate text-[11px] text-fg">
              Merge {{ pr.repo }}#{{ pr.number }} “{{ pr.title }}”?
            </span>
            <button
              type="button"
              :data-testid="`cockpit-github-merge-confirm-${pr.number}`"
              :disabled="merging !== null"
              class="shrink-0 rounded-md border border-danger px-2 py-0.5 text-[11px] text-danger-text hover:brightness-110 disabled:opacity-60"
              @click="confirmMerge(pr)"
            >
              {{ merging === `${pr.repo}#${pr.number}` ? 'Merging…' : 'Confirm' }}
            </button>
            <button
              type="button"
              :data-testid="`cockpit-github-merge-cancel-${pr.number}`"
              class="shrink-0 rounded-md border border-line px-2 py-0.5 text-[11px] text-fg-mute hover:text-fg"
              @click="pending = null"
            >
              Cancel
            </button>
          </span>
        </div>
      </li>
    </ul>
    <p
      v-if="mergeError"
      data-testid="cockpit-github-merge-error"
      role="alert"
      class="text-[12px] rounded-md px-3 py-2 mt-2 bg-warning-soft text-warning-text"
    >
      {{ mergeError }}
    </p>
  </CockpitPanel>
</template>
