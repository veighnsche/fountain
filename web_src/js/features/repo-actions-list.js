import $ from 'jquery';
import {contributionRootSelector} from '../native-lifecycle.js';

export function pollingOk() {
  if (document.visibilityState !== 'visible' || document.querySelector('[aria-expanded=true]')) return false;
  const form = document.getElementById('branch-dropdown-form');
  return !form?.checkVisibility();
}

// The native HTMX trigger and delegated handler query the current page.
// They do not capture a retired workflow dispatch element.
let actionsListBound = false;

function onBeforeActionsPoll(event) {
  const source = event.detail?.elt;
  if (!source?.matches?.('[data-native-actions-poll]') || source.closest(contributionRootSelector)) return;
  const visibilityChange = event.detail.requestConfig?.triggeringEvent?.type === 'visibilitychange';
  if (visibilityChange ? document.visibilityState !== 'visible' : !pollingOk()) event.preventDefault();
}

export function initRepoActionsList() {
  window.pollingOk = pollingOk;
  if (actionsListBound) return;
  actionsListBound = true;
  document.addEventListener('htmx:beforeRequest', onBeforeActionsPoll);
  document.addEventListener('click', (event) => {
    const dropdown = document.getElementById('workflow_dispatch_dropdown');
    if (!dropdown || dropdown.closest(contributionRootSelector)) return;
    const menu = dropdown.querySelector(':scope > .menu');
    if (!menu || menu.contains(event.target)) return;
    const inside = dropdown.contains(event.target);
    if (!inside && !menu.classList.contains('visible')) return;
    if (inside) event.stopPropagation();
    const close = menu.classList.contains('visible');
    $(menu).transition({animation: `slide down ${close ? 'out' : 'in'}`, duration: 200, queue: !close});
  });
}
