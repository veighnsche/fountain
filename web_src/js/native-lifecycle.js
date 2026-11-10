// Native page-feature lifecycle seam (lane B).
//
// The H-0 contract with lane A: A's transition mechanism calls
// retirePageFeatures() before swapping native content, then setPageConfig()
// with the incoming document's page data and initPageFeatures() after.
// Global features initialize once per document and are never retired by
// navigation. Registered page features must not double-initialize: re-entry
// without retire is a no-op, and retire runs in reverse registration order.

import {createFeatureRun} from './native-feature-run.js';
export {setPageConfig} from './native-page-config.js';

// Native features must not bind or retire contribution-owned subtrees: the
// persistent panels home (including parked roots) and page/panel roots.
export const contributionRootSelector = '[data-extension-panels],[data-extension-page],[data-extension-panel]';

const pageFeatures = new Map();
const globalFeatures = new Map();
const activePageFeatures = new Map();
const pageRuns = new Set();
let globalsInitialized = false;
let pageGeneration = 0;
let nativeNavigator = null;

export function registerPageFeature(name, {init, retire}) {
  if (pageFeatures.has(name)) throw new Error(`native feature "${name}" is already registered`);
  if (typeof init !== 'function') throw new Error(`native feature "${name}" needs an init function`);
  if (retire !== undefined && typeof retire !== 'function') throw new Error(`native feature "${name}" needs a retire function or none`);
  pageFeatures.set(name, {init, retire});
}

export function registerGlobalFeature(name, init) {
  if (globalFeatures.has(name)) throw new Error(`native feature "${name}" is already registered`);
  if (typeof init !== 'function') throw new Error(`native feature "${name}" needs an init function`);
  globalFeatures.set(name, init);
  // A global registered after document init still needs its one initialization.
  if (globalsInitialized) init();
}

export function initGlobalFeatures() {
  if (globalsInitialized) return;
  globalsInitialized = true;
  for (const init of globalFeatures.values()) {
    init();
  }
}

export function initPageFeatures() {
  for (const [name, feature] of pageFeatures) {
    if (activePageFeatures.has(name)) continue;
    const run = createPageFeatureRun();
    const active = {feature, run};
    activePageFeatures.set(name, active);
    try {
      const result = feature.init(run);
      Promise.resolve(result).catch((error) => {
        try {
          stopFeature(name, active);
        } catch (cleanupError) {
          console.error(`Could not retire native feature "${name}"`, cleanupError);
        }
        if (error?.name !== 'AbortError') console.error(`Could not initialize native feature "${name}"`, error);
      });
    } catch (error) {
      try {
        stopFeature(name, active);
      } catch (cleanupError) {
        console.error(`Could not retire native feature "${name}"`, cleanupError);
      }
      throw error;
    }
  }
}

function stopFeature(name, active) {
  let firstError;
  try {
    active.run.dispose();
  } catch (error) {
    firstError = error;
  }
  if (activePageFeatures.get(name) === active) {
    activePageFeatures.delete(name);
    try {
      active.feature.retire?.();
    } catch (error) {
      firstError ??= error;
    }
  }
  if (firstError) throw firstError;
}

export function retirePageFeatures() {
  pageGeneration++;
  const retiringRuns = Array.from(pageRuns).reverse();
  let firstError;
  for (const [name, active] of Array.from(activePageFeatures).reverse()) {
    try {
      stopFeature(name, active);
    } catch (error) {
      firstError ??= error;
    }
  }
  for (const run of retiringRuns) {
    try {
      run.dispose();
    } catch (error) {
      firstError ??= error;
    }
  }
  if (firstError) throw firstError;
}

export function createPageFeatureRun() {
  const generation = pageGeneration;
  const run = createFeatureRun(() => generation === pageGeneration);
  pageRuns.add(run);
  run.own(() => pageRuns.delete(run));
  return run;
}

// Native leaf templates contribute contextual data without executing scripts.
// Use the same envelope on document boot and on an entered native render.
export function readNativePageData(native) {
  const embedded = native.body?.dataset.pageData;
  if (typeof embedded !== 'string' || !embedded) return null;
  try {
    let data = JSON.parse(embedded) ?? {};
    if (typeof data !== 'object' || Array.isArray(data)) return null;
    for (const script of native.querySelectorAll('script[data-native-page-data]')) {
      if (script.closest(contributionRootSelector)) continue;
      const key = script.dataset.nativePageData;
      const value = JSON.parse(script.textContent);
      if (!key || !value || typeof value !== 'object') return null;
      const field = script.hasAttribute('data-native-page-data-list') ?
        [...(data[key] ?? []), value] : Array.isArray(value) ? value : {...data[key], ...value};
      data = {...data, [key]: field};
    }
    return data;
  } catch {
    return null;
  }
}

export function getPageData() {
  return window.config.pageData;
}

// Monotonic counter bumped by every retire. Async callbacks capture it at
// start and drop their result when it no longer matches.
export function currentPageGeneration() {
  return pageGeneration;
}

// The host transition plugs its transplant here; sort-style native
// navigations then stay in-shell. Plain pages without a host keep the native
// history-replacing load.
export function setNativeNavigator(navigate) {
  if (navigate !== null && typeof navigate !== 'function') throw new Error('native navigator must be a function or null');
  nativeNavigator = navigate;
}

export function navigateNative(url, {replace = true} = {}) {
  if (nativeNavigator) nativeNavigator(url, {replace});
  else if (replace) window.location.replace(url);
  else window.location.assign(url);
}
