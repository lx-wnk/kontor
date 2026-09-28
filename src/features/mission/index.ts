// LiveWorkWidget is exported because the workspace widget registry places it
// as a cross-feature widget, same as the cockpit panels.
export { default as KontorWidget } from './components/KontorWidget.vue'
export { default as LiveWorkWidget } from './components/LiveWorkWidget.vue'
export { default as NeedsYouQueue } from './components/NeedsYouQueue.vue'
export { useKontorAgent, useKontorSession } from './composables/useKontorSession'
export type { NextKind, NextThing } from './composables/useNextThing'
export { rankNextThings } from './composables/useNextThing'
