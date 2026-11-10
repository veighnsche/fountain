import {retirePageFeatures, setPageConfig, readNativePageData, initPageFeatures, setNativeNavigator} from '../native-lifecycle.js';
import {submitEventSubmitter} from '../utils/dom.js';
import {mountContribution, parseMountDataset, mountIdentity, mountOwnerKey} from './extension-mount.js';
import {classifyNavigation, validAppSubURL, appSubURLPathname, matchesDirectPattern} from './extension-navigation-policy.js';

/** Classify a browser destination for host-handled native navigation. */
export function classifyWorkspaceNavigation(input, base, origin, appSubPath = '') {
  return classifyNavigation(input, base, origin, appSubPath);
}

export function workspacePath(input, base, origin, appSubPath = '') {
  if (!input?.startsWith('/') || input.startsWith('//')) return null;
  const result = classifyWorkspaceNavigation(input, base, origin, appSubPath);
  return result.kind === 'native' ? result.path : null;
}

function matchDirectPattern(path, pattern) {
  const parts = path.replace(/\/$/, '').split('/');
  const segments = pattern.split('/');
  if (parts.length !== segments.length) return false;
  return segments.every((segment, i) => {
    if (segment.startsWith('{') && segment.endsWith('}')) return parts[i] !== '' && parts[i].length <= 255;
    return segment === parts[i];
  });
}

/**
 * Report whether a pathname is a workspace root or matches one of the
 * declared generic direct patterns the host publishes from manifest
 * subroute declarations (NFX-C04/H-ROUTE), e.g.
 * "/workspace/requests/{request}". Parameter values are bounded opaque
 * segments: no product-ID shape rule is applied here. Declared patterns
 * also admit their repository form "/{owner}/{repo}/workspace/...": the
 * host registers those routes generically (repositorySubroute) and the
 * global owner's content routes never claim the repository namespace.
 */
export function workspaceDirectPath(pathname, appSubPath = '', patterns = []) {
  if (pathname.endsWith('/workspace')) return true;
  if (!pathname.startsWith(appSubPath)) return false;
  const rest = pathname.slice(appSubPath.length);
  if (!rest.startsWith('/')) return false;
  if (patterns.some((pattern) => typeof pattern === 'string' && matchDirectPattern(rest, pattern))) return true;
  const segments = rest.split('/').filter((segment) => segment !== '');
  if (segments.length < 4 || segments[2] !== 'workspace') return false;
  const candidate = `/workspace/${segments.slice(3).join('/')}`;
  return patterns.some((pattern) => typeof pattern === 'string' && matchDirectPattern(candidate, pattern));
}

function ownerDirectPath(pathname, root) {
  const parsed = parseMountDataset(root);
  if (!parsed.ok || !parsed.ownerId) return false;
  const prefix = appSubURLPathname(parsed.appSubUrl);
  return pathname.startsWith(`${prefix}/`) && matchesDirectPattern(pathname.slice(prefix.length), parsed.directPatterns);
}

const importExtension = (url) => import(/* webpackIgnore: true */ url);
const sessionCheckPath = (appSubPath) => `${appSubPath}/-/native/session`;

function focusFragment(hash) {
  let id = hash.slice(1);
  try {
    id = decodeURIComponent(id);
  } catch {
    // Keep the raw fragment when it is not valid percent-encoding.
  }
  const anchor = document.getElementById(id) ?? document.getElementById(`user-content-${id}`) ?? document.getElementsByName(`user-content-${id}`)[0];
  anchor?.scrollIntoView();
  if (anchor?.matches('a[href], button, input, select, textarea, [tabindex]')) anchor.focus({preventScroll: true});
}

// Mature dirty tracking marks native forms with .dirty (default) or
// .dirty-file (editor silent). Only the transplant region matters: the
// persistent home and parked page roots survive, navbar/footer sync in
// place. Ignore-dirty forms are explicitly exempt. Never dispatch global
// beforeunload here: stopwatch/notification close workers there.
function hasDirtyNativeForm(excludeForm = null) {
  const region = document.querySelector('.page-content');
  if (!region) return false;
  for (const form of region.querySelectorAll('form.dirty, form.dirty-file')) {
    if (form === excludeForm || !form.isConnected) continue;
    if (form.classList.contains('ignore-dirty')) continue;
    if (form.closest('[data-extension-page],[data-extension-panel]')) continue;
    return true;
  }
  return false;
}

function rootIdentity(root, options) {
  return mountIdentity(parseMountDataset(root), options);
}

function ownerKey(root) {
  return mountOwnerKey(parseMountDataset(root));
}

function contributionSlot(parsed) {
  return parsed.mode ? JSON.stringify([parsed.mode, parsed.extensionId, parsed.contributionId]) : null;
}

function refreshMountPresentation(root, peer) {
  for (const name of ['extensionRepositoryId', 'extensionDirectPatterns']) {
    if (peer.dataset[name] === undefined) delete root.dataset[name];
    else root.dataset[name] = peer.dataset[name];
  }
}

function liveSessionMatches(url, generation, fetcher) {
  return Promise.resolve().then(() => fetcher(url, {credentials: 'same-origin', cache: 'no-store', redirect: 'manual'}))
    .then((response) => response.status === 204 && response.headers.get('X-Extension-Session-Generation') === generation)
    .catch(() => false);
}

const documentControllers = new WeakMap();
const defaultFetcher = (...args) => window.fetch(...args);
const historyIndexKey = '__workspaceNavIndex';
const sessionChangedMessage = 'Your session changed. Reload Forgejo to continue.';

function report(errors) {
  for (const {message, error} of errors) console.error(message, error);
}

function callOwner(owner, method, errors, ...args) {
  try {
    owner[method](...args);
  } catch (error) {
    const message = method === 'retire' && args[0] === 'authority-revoked' ?
      'Could not retire withdrawn contribution' : `Could not ${method} retained contribution`;
    errors.push({message, error});
  }
}

// Withdraw tracking before calling contribution code. A throwing callback
// cannot keep a record authoritative or prevent the remaining cleanup.
function terminate(list, reason, dispose, errors) {
  const retired = [];
  for (const record of list) {
    if (record.status === 'terminal') continue;
    retired.push({owner: record.owner, controller: record.controller});
    record.status = 'terminal';
    record.owner = null;
    record.controller = null;
    record.root.removeAttribute('data-extension-mounted');
  }
  for (const {owner, controller} of retired) {
    if (owner) callOwner(owner, dispose ? 'dispose' : 'retire', errors, reason);
    else controller?.abort();
  }
}

function documentController() {
  let controller = documentControllers.get(document);
  if (controller) return controller;
  const home = document.querySelector('[data-extension-panels]');
  const firstRoot = document.querySelector('[data-extension-page], [data-extension-panel]');
  if (!home && !firstRoot) return null;
  controller = createDocumentController(home, firstRoot);
  documentControllers.set(document, controller);
  return controller;
}

export function initExtensionPages(importer = importExtension, fetcher = defaultFetcher) {
  documentController()?.enablePages(importer, fetcher);
}

export function initExtensionWorkspace(fetcher = defaultFetcher) {
  if (!document.querySelector('[data-extension-panels]')) return;
  documentController()?.enableWorkspace(fetcher);
}

function createDocumentController(home, firstRoot) {
  const admitted = home ?? firstRoot;
  const generation = admitted.dataset.extensionSessionGeneration;
  const appSubPath = appSubURLPathname(admitted.dataset.appSubUrl);
  const origin = window.location.origin;
  const liveContent = () => document.querySelector('.page-content');
  const livePath = () => `${window.location.pathname}${window.location.search}${window.location.hash}`;
  const records = new Map();
  const panelOwnerKeys = new Set();
  const documentHolds = new Set();
  let park = home?.querySelector('[data-extension-page-park]') ?? null;
  let pagesEnabled = false;
  let workspaceEnabled = false;
  let importer;
  let pagesFetcher = defaultFetcher;
  let workspaceFetcher;
  let closed = false;
  let invalidated = false;
  let nativeInvalidated = false;
  let checkVersion = 0;
  let pendingCheck = null;
  let navigationVersion = 0;
  let navigation = null;
  let current = livePath();
  let currentIndex = typeof window.history.state?.[historyIndexKey] === 'number' ? window.history.state[historyIndexKey] : 0;
  let restoration = null;
  let historyAvailable = false;
  let originalPushState;
  let originalReplaceState;
  let pushState;
  let replaceState;

  function setHold(record, hold, held, errors) {
    if (record.status === 'terminal') return;
    if (held) record.holds.add(hold);
    else record.holds.delete(hold);
    if (!record.owner) return;
    const suspended = record.holds.size !== 0;
    if (record.suspended === suspended) return;
    record.suspended = suspended;
    if (suspended) callOwner(record.owner, 'suspend', errors);
    else callOwner(record.owner, 'resume', errors, {sessionGeneration: generation});
  }

  function concealTargets() {
    const targets = Array.from(document.querySelectorAll(home ? '[data-extension-page]' : '[data-extension-page], [data-extension-panel]'));
    if (home) targets.push(home);
    if (workspaceEnabled && liveContent()) targets.push(liveContent());
    return targets;
  }

  function syncConcealment() {
    if (invalidated || closed) return;
    for (const root of concealTargets()) root.style.display = documentHolds.size ? 'none' : '';
  }

  function setDocumentHold(hold, held) {
    if (held) documentHolds.add(hold);
    else documentHolds.delete(hold);
    const errors = [];
    for (const record of records.values()) setHold(record, hold, held, errors);
    syncConcealment();
    report(errors);
  }

  function presentation(record, facts) {
    const isPage = record.parsed.mode === 'page';
    const visible = isPage ? !record.holds.has('shelter') : !home?.hasAttribute('hidden');
    const routePath = typeof facts.route === 'string' ? facts.route.split(/[?#]/)[0] : '';
    const direct = !isPage && visible && ownerDirectPath(routePath, record.root);
    return {...facts, mode: isPage || direct ? 'page' : 'panel', visible};
  }

  function routeFacts() {
    return {
      route: livePath(), params: Object.fromEntries(new URLSearchParams(window.location.search)),
      ...(home?.dataset.extensionRepositoryId ? {repositoryId: home.dataset.extensionRepositoryId} : {}),
    };
  }

  function mountRoot(root) {
    if (closed || invalidated || records.has(root)) return;
    const parsed = parseMountDataset(root);
    // Shared owners defer effects until their retained presentation opens.
    if (parsed.mode === 'panel' && parsed.ownerId && home?.hasAttribute('hidden')) return;
    if (parsed.mode === 'page' && panelOwnerKeys.has(mountOwnerKey(parsed))) {
      root.setAttribute('data-extension-owner-presented', '');
      return;
    }
    root.removeAttribute('data-extension-owner-presented');
    const record = {
      root, parsed, identity: mountIdentity(parsed), status: 'pending',
      controller: new AbortController(), owner: null, suspended: false,
      holds: new Set(documentHolds), facts: null,
    };
    records.set(root, record);
    root.setAttribute('data-extension-mounted', '');
    syncConcealment();
    mountContribution(root, {importer, fetcher: pagesFetcher, signal: record.controller.signal}).then((owner) => {
      const errors = [];
      if (closed || invalidated || record.status === 'terminal' || records.get(root) !== record ||
          !root.isConnected || record.identity !== rootIdentity(root)) {
        if (owner) callOwner(owner, 'dispose', errors);
        terminate([record], 'contribution-unavailable', true, errors);
        report(errors);
        return;
      }
      record.controller = null;
      if (!owner) {
        record.status = 'terminal';
        return;
      }
      record.owner = owner;
      record.status = record.holds.has('shelter') ? 'sheltered' : 'mounted';
      if (record.holds.size) setHold(record, 'session', documentHolds.has('session'), errors);
      if (record.facts) callOwner(owner, 'update', errors, presentation(record, record.facts));
      else if (parsed.mode === 'panel') {
        const facts = routeFacts();
        if (parsed.repositoryId) facts.repositoryId = parsed.repositoryId;
        if (home?.hasAttribute('hidden') || ownerDirectPath(window.location.pathname, root)) {
          callOwner(owner, 'update', errors, presentation(record, facts));
        }
      }
      report(errors);
    }).catch((error) => {
      const errors = [{message: 'Could not mount retained contribution', error}];
      terminate([record], 'contribution-unavailable', true, errors);
      report(errors);
    });
  }

  function parkPageRoots(errors) {
    if (!park) {
      park = document.createElement('div');
      park.setAttribute('data-extension-page-park', '');
      park.setAttribute('hidden', '');
      (home ?? document.body).append(park);
    }
    for (const record of records.values()) {
      if (record.status !== 'mounted' || record.parsed.mode !== 'page' || !record.root.isConnected) continue;
      record.status = 'sheltered';
      setHold(record, 'shelter', true, errors);
      park.append(record.root);
    }
    // Preserve the four-page oldest-evictable policy and persist exemption.
    while (park.children.length > 4) {
      const oldest = Array.from(park.children).find((root) => !root.hasAttribute('data-extension-persist'));
      if (!oldest) break;
      oldest.remove();
      const record = records.get(oldest);
      if (record) terminate([record], 'contribution-unavailable', true, errors);
      records.delete(oldest);
    }
  }

  function reconcilePanels(enteredHome, errors) {
    if (!home) return;
    const entered = new Map();
    for (const root of enteredHome.querySelectorAll('[data-extension-panel]')) {
      const parsed = parseMountDataset(root);
      if (!parsed.ok || parsed.sessionGeneration !== generation || appSubURLPathname(parsed.appSubUrl) !== appSubPath) continue;
      entered.set(mountIdentity(parsed), root);
    }
    panelOwnerKeys.clear();
    for (const root of entered.values()) {
      const key = ownerKey(root);
      if (key) panelOwnerKeys.add(key);
    }
    for (const root of home.querySelectorAll('[data-extension-panel]')) {
      const peer = entered.get(rootIdentity(root));
      if (peer) {
        entered.delete(rootIdentity(root));
        refreshMountPresentation(root, peer);
        continue;
      }
      root.setAttribute('hidden', '');
      root.setAttribute('inert', '');
      const section = root.closest('.extension-workspace-panel');
      (section?.parentElement === home ? section : root).remove();
      const record = records.get(root);
      if (record) terminate([record], 'authority-revoked', false, errors);
      records.delete(root);
    }
    for (const root of entered.values()) {
      const section = root.closest('.extension-workspace-panel');
      home.append(section?.parentElement === enteredHome ? section : root);
      mountRoot(root);
    }
  }

  function prepareContributionRoots(event) {
    if (!pagesEnabled || closed || invalidated) return;
    const errors = [];
    if (event.detail?.panelsHome) reconcilePanels(event.detail.panelsHome, errors);
    parkPageRoots(errors);
    report(errors);
  }

  function syncContributionRoots(facts) {
    if (!pagesEnabled || closed || invalidated) return;
    const errors = [];
    for (const record of records.values()) {
      if (record.status === 'terminal') {
        if (!record.root.isConnected) records.delete(record.root);
      } else if (!record.root.isConnected || record.identity !== rootIdentity(record.root)) {
        terminate([record], 'contribution-unavailable', record.parsed.mode === 'page', errors);
        records.delete(record.root);
      }
    }
    const entered = new Map();
    const enteredLoose = new Map();
    const enteredSlots = new Map();
    const refused = [];
    for (const root of document.querySelectorAll('[data-extension-page]')) {
      if (park?.contains(root)) continue;
      const parsed = parseMountDataset(root);
      const identity = mountIdentity(parsed);
      if (identity) {
        if (!entered.has(identity)) entered.set(identity, root);
        else mountRoot(root);
      } else refused.push(root);
      const loose = mountIdentity(parsed, {includeContractVersion: false});
      if (loose && !enteredLoose.has(loose)) enteredLoose.set(loose, root);
      const slot = contributionSlot(parsed);
      if (slot) enteredSlots.set(slot, root);
    }
    const owned = new Set();
    for (const record of records.values()) {
      if (record.status === 'terminal') continue;
      if (record.status === 'sheltered') {
        const duplicate = entered.get(record.identity);
        if (duplicate && duplicate !== record.root) {
          entered.delete(record.identity);
          duplicate.replaceWith(record.root);
          refreshMountPresentation(record.root, duplicate);
          record.status = 'mounted';
          record.facts = facts;
          owned.add(record.root);
          callOwner(record.owner, 'update', errors, {...facts, mode: 'page', visible: true});
          setHold(record, 'shelter', false, errors);
          continue;
        }
        const skewed = enteredLoose.get(mountIdentity(record.parsed, {includeContractVersion: false}));
        if (skewed && skewed !== record.root) {
          entered.delete(rootIdentity(skewed));
          skewed.remove();
          record.root.remove();
          terminate([record], 'contribution-unavailable', false, errors);
          continue;
        }
        if (enteredSlots.has(contributionSlot(record.parsed))) {
          record.root.remove();
          terminate([record], 'contribution-unavailable', false, errors);
          continue;
        }
      }
      owned.add(record.root);
      record.facts = facts;
      if (record.owner) callOwner(record.owner, 'update', errors, presentation(record, facts));
    }
    for (const root of entered.values()) if (!owned.has(root)) mountRoot(root);
    for (const root of refused) if (root.isConnected) mountRoot(root);
    if (!home?.hasAttribute('hidden')) {
      for (const root of home?.querySelectorAll('[data-extension-panel]') ?? []) mountRoot(root);
    }
    syncConcealment();
    report(errors);
  }

  function cancelNavigation() {
    navigationVersion++;
    if (navigation) {
      clearTimeout(navigation.timer);
      navigation.controller.abort();
      navigation = null;
    }
  }

  function invalidateNativeContent(errors) {
    if (!workspaceEnabled || nativeInvalidated) return;
    nativeInvalidated = true;
    home.setAttribute('hidden', '');
    home.setAttribute('inert', '');
    try {
      retirePageFeatures();
    } catch (error) {
      errors.push({message: 'Could not retire native page features', error});
    } finally {
      setPageConfig({});
      const content = liveContent();
      if (content) {
        content.replaceChildren();
        content.textContent = sessionChangedMessage;
        content.style.display = '';
      }
    }
  }

  function invalidate() {
    if (closed) return;
    const errors = [];
    if (invalidated) {
      invalidateNativeContent(errors);
      report(errors);
      return;
    }
    invalidated = true;
    cancelNavigation();
    checkVersion++;
    pendingCheck = null;
    const targets = concealTargets();
    for (const root of targets) root.style.display = 'none';
    terminate(Array.from(records.values()), 'session-changed', false, errors);
    records.clear();
    if (workspaceEnabled) invalidateNativeContent(errors);
    else {
      for (const root of targets) {
        root.replaceChildren();
        root.textContent = sessionChangedMessage;
        root.style.display = '';
      }
    }
    window.dispatchEvent(new Event('extension-workspace-invalidated'));
    report(errors);
  }

  function checkSession() {
    if (invalidated || closed) return;
    if (pendingCheck) return pendingCheck.promise;
    const version = ++checkVersion;
    const check = {promise: null};
    pendingCheck = check;
    setDocumentHold('session', true);
    if (document.visibilityState === 'visible') setDocumentHold('hidden', false);
    check.promise = liveSessionMatches(new URL(sessionCheckPath(appSubPath), origin).href, generation, workspaceFetcher ?? pagesFetcher)
      .then((matches) => {
        if (closed || invalidated || version !== checkVersion) return;
        if (!matches) invalidate();
        else setDocumentHold('session', false);
      }).finally(() => {
        if (pendingCheck === check) pendingCheck = null;
      });
    return check.promise;
  }

  function syncPanelsHome(enteredHome) {
    if (!enteredHome.hasAttribute('hidden')) {
      home.removeAttribute('hidden');
      home.removeAttribute('inert');
      return;
    }
    const focusInside = home.contains(document.activeElement);
    home.setAttribute('hidden', '');
    home.setAttribute('inert', '');
    if (focusInside) {
      const content = liveContent();
      if (content) {
        content.setAttribute('tabindex', '-1');
        content.focus({preventScroll: true});
      }
    }
  }

  function syncNavbarStates(native) {
    const live = document.getElementById('navbar');
    const incoming = native.getElementById('navbar');
    if (!live || !incoming) return;
    const incomingByHref = new Map();
    for (const link of incoming.querySelectorAll('a[href]')) incomingByHref.set(new URL(link.getAttribute('href'), origin).href, link);
    for (const link of live.querySelectorAll('a[href]')) {
      const peer = incomingByHref.get(new URL(link.getAttribute('href'), origin).href);
      link.classList.toggle('active', Boolean(peer?.classList.contains('active')));
      if (peer?.hasAttribute('aria-current')) link.setAttribute('aria-current', peer.getAttribute('aria-current'));
      else link.removeAttribute('aria-current');
    }
  }

  function intentFor(decision, source, history, extra = {}) {
    return {
      target: new URL(decision.url, origin), path: decision.path,
      source, history, from: {route: current, index: currentIndex}, ...extra,
    };
  }

  function fullLoad(intent, committed = false) {
    if (closed || invalidated) return;
    const method = intent.history === 'push' && !committed ? 'assign' : 'replace';
    window.location[method](intent.target.href);
  }

  function writeHistory(intent, outgoingScrollY) {
    try {
      if (intent.history === 'push') {
        originalReplaceState.call(window.history, {...window.history.state, scrollY: outgoingScrollY, [historyIndexKey]: currentIndex}, '', window.location.href);
        originalPushState.call(window.history, {...window.history.state, scrollY: 0, [historyIndexKey]: currentIndex + 1}, '', intent.path);
        currentIndex++;
      } else if (intent.history === 'replace') {
        if (intent.target.href !== window.location.href) {
          originalReplaceState.call(window.history, {...window.history.state, [historyIndexKey]: currentIndex}, '', intent.path);
        }
      } else if (typeof intent.landedIndex === 'number') currentIndex = intent.landedIndex;
      return true;
    } catch {
      fullLoad(intent);
      return false;
    }
  }

  function restoreTraversal(intent) {
    if (intent.step) {
      restoration = {route: intent.from.route, index: intent.from.index};
      try {
        window.history.go(-intent.step);
        return;
      } catch {
        restoration = null;
      }
    }
    // No reliable physical restoration remains. Reload the displayed route
    // in place rather than rewriting a landed entry and continuing stale.
    fullLoad({...intent, target: new URL(intent.from.route, origin), history: 'replace'});
  }

  async function transplant(intent) {
    if (closed || invalidated) return false;
    cancelNavigation();
    if (!historyAvailable) {
      fullLoad(intent);
      return false;
    }
    const target = intent.target;
    const previous = new URL(current, origin);
    if (!intent.refresh && intent.history !== 'traverse' && target.href === previous.href) {
      if (target.hash) focusFragment(target.hash);
      return true;
    }
    if ((intent.history !== 'traverse' || typeof intent.landedIndex === 'number') &&
        target.pathname === previous.pathname && target.search === previous.search && target.hash !== previous.hash) {
      if (!writeHistory(intent, window.scrollY)) return false;
      current = intent.path;
      if (target.hash) focusFragment(target.hash);
      if (intent.history !== 'traverse') window.dispatchEvent(new HashChangeEvent('hashchange', {oldURL: previous.href, newURL: target.href}));
      announceNavigation(intent.path, home.dataset.extensionRepositoryId);
      return true;
    }
    if (hasDirtyNativeForm(intent.submittedForm)) {
      let confirmed = false;
      try {
        confirmed = window.confirm?.('You have unsaved changes!') ?? false;
      } catch {
        confirmed = false;
      }
      if (!confirmed) {
        if (intent.history === 'traverse') restoreTraversal(intent);
        return false;
      }
    }
    // A foreign entry has no position witness for later compensation.
    // Keep its existing slot and let a full document own it afresh.
    if (intent.history === 'traverse' && typeof intent.landedIndex !== 'number') {
      fullLoad(intent);
      return false;
    }
    const attempt = {controller: new AbortController(), version: navigationVersion, timer: null, timedOut: false};
    navigation = attempt;
    const isCurrent = () => !closed && !invalidated && attempt.version === navigationVersion && navigation === attempt;
    const fallback = (committed = false) => {
      if (isCurrent() && (attempt.timedOut || !attempt.controller.signal.aborted)) fullLoad(intent, committed);
    };
    attempt.timer = setTimeout(() => {
      if (!isCurrent()) return;
      attempt.timedOut = true;
      attempt.controller.abort();
      fallback();
      navigation = null;
    }, 15000);
    let historyCommitted = false;
    try {
      const response = await workspaceFetcher(target.href, {
        credentials: 'same-origin', cache: 'no-store', redirect: 'error',
        headers: {Accept: 'text/html', 'X-Extension-Workspace-Navigation': '1'}, signal: attempt.controller.signal,
      });
      if (!isCurrent() || attempt.controller.signal.aborted) return false;
      if (!response.ok) {
        fallback();
        return false;
      }
      if (response.headers.get('X-Extension-Session-Generation') !== generation) {
        invalidate();
        return false;
      }
      const html = await response.text();
      if (!isCurrent() || attempt.controller.signal.aborted) return false;
      const native = new DOMParser().parseFromString(html, 'text/html');
      const live = liveContent();
      const content = native.querySelector('.page-content');
      const enteredHome = native.querySelector('[data-extension-panels]');
      const pageData = readNativePageData(native);
      if (!live || !content || !pageData || !enteredHome) {
        fallback();
        return false;
      }
      if (enteredHome.dataset.extensionSessionGeneration !== generation) {
        invalidate();
        return false;
      }
      const repositoryId = enteredHome.dataset.extensionRepositoryId ?? content.querySelector('[data-extension-page]')?.dataset.extensionRepositoryId ?? '';
      const focusInContent = live.contains(document.activeElement);
      if (!writeHistory(intent, window.scrollY)) return false;
      historyCommitted = true;
      window.dispatchEvent(new CustomEvent('extension-workspace-will-navigate', {detail: {panelsHome: enteredHome}}));
      retirePageFeatures();
      setPageConfig(pageData);
      document.body.dataset.pageData = JSON.stringify(pageData);
      document.title = native.title;
      current = intent.path;
      live.replaceWith(content);
      if (focusInContent) {
        content.setAttribute('tabindex', '-1');
        content.focus({preventScroll: true});
      }
      syncNavbarStates(native);
      if (enteredHome.dataset.extensionSubroutes === undefined) delete home.dataset.extensionSubroutes;
      else home.dataset.extensionSubroutes = enteredHome.dataset.extensionSubroutes;
      syncPanelsHome(enteredHome);
      if (repositoryId) home.dataset.extensionRepositoryId = repositoryId;
      else delete home.dataset.extensionRepositoryId;
      syncConcealment();
      initPageFeatures();
      if (intent.history === 'push') {
        if (target.hash) focusFragment(target.hash);
        else window.scrollTo(0, 0);
      }
      announceNavigation(intent.path, repositoryId);
      return true;
    } catch {
      fallback(historyCommitted);
      return false;
    } finally {
      clearTimeout(attempt.timer);
      if (navigation === attempt) navigation = null;
    }
  }

  function announceNavigation(path, repositoryId) {
    home.dispatchEvent(new CustomEvent('extension-workspace-navigation', {bubbles: true, detail: {
      route: path, params: Object.fromEntries(new URL(path, origin).searchParams),
      ...(repositoryId ? {repositoryId} : {}),
    }}));
  }

  function onWorkspaceClick(event) {
    if (invalidated || closed || event.defaultPrevented || event.button !== 0 || event.metaKey || event.ctrlKey || event.shiftKey || event.altKey) return;
    const link = event.composedPath().find((node) => node?.localName === 'a' && 'href' in node);
    if (!link || link.hasAttribute('download') || (link.target && link.target !== '_self')) return;
    const decision = classifyWorkspaceNavigation(link.href, window.location.href, origin, appSubPath);
    if (decision.kind === 'reject') {
      event.preventDefault();
      return;
    }
    if (!historyAvailable || decision.kind !== 'native') return;
    event.preventDefault();
    transplant(intentFor(decision, 'link', 'push'));
  }

  function onWorkspaceSubmit(event) {
    if (!historyAvailable || invalidated || closed || event.defaultPrevented) return;
    const form = event.target;
    if (!(form instanceof HTMLFormElement) || form.target && form.target !== '_self') return;
    const submitter = submitEventSubmitter(event);
    const method = (submitter?.getAttribute('formmethod') || form.getAttribute('method') || 'get').toLowerCase();
    if (method !== 'get') return;
    const formData = new FormData(form);
    if (submitter?.getAttribute('name')) formData.append(submitter.getAttribute('name'), submitter.getAttribute('value') || '');
    const target = new URL(submitter?.getAttribute('formaction') || form.getAttribute('action') || window.location.href, window.location.href);
    const params = new URLSearchParams();
    for (const [key, value] of formData) params.append(key, value.toString());
    target.search = params.toString();
    const decision = classifyWorkspaceNavigation(target.href, window.location.href, origin, appSubPath);
    if (decision.kind !== 'native') return;
    event.preventDefault();
    transplant(intentFor(decision, 'get-form', 'push', {submittedForm: form, refresh: true}));
  }

  function onPopState() {
    if (closed || invalidated || !historyAvailable) return;
    cancelNavigation();
    const landedIndex = window.history.state?.[historyIndexKey];
    const path = livePath();
    if (restoration) {
      const expected = restoration;
      restoration = null;
      if (path === expected.route && landedIndex === expected.index) return;
      fullLoad({target: new URL(expected.route, origin), history: 'replace'});
      return;
    }
    const decision = classifyWorkspaceNavigation(path, window.location.href, origin, appSubPath);
    if (decision.kind !== 'native') {
      if (decision.kind === 'top') fullLoad(intentFor(decision, 'history', 'traverse'));
      else restoreTraversal({from: {route: current, index: currentIndex}, step: typeof landedIndex === 'number' ? landedIndex - currentIndex : 0});
      return;
    }
    if (path === current) {
      if (typeof landedIndex === 'number') currentIndex = landedIndex;
      else fullLoad(intentFor(decision, 'history', 'traverse'));
      return;
    }
    const intent = intentFor(decision, 'history', 'traverse', {landedIndex, step: typeof landedIndex === 'number' ? landedIndex - currentIndex : 0});
    transplant(intent).then((proceeded) => {
      if (proceeded && !closed && !invalidated && current === path) window.scrollTo(0, window.history.state?.scrollY ?? 0);
    });
  }

  function enableHistory() {
    originalPushState = window.history.pushState;
    originalReplaceState = window.history.replaceState;
    try {
      originalReplaceState.call(window.history, {...window.history.state, [historyIndexKey]: currentIndex}, '', window.location.href);
    } catch {
      return;
    }
    historyAvailable = true;
    pushState = (state, title, url) => {
      const stamped = {...(state && typeof state === 'object' ? state : {}), scrollY: 0, [historyIndexKey]: currentIndex + 1};
      originalPushState.call(window.history, stamped, title, url);
      currentIndex++;
      current = livePath();
    };
    replaceState = (state, title, url) => {
      const stamped = {...(state && typeof state === 'object' ? state : {}), [historyIndexKey]: currentIndex};
      originalReplaceState.call(window.history, stamped, title, url);
      current = livePath();
    };
    window.history.pushState = pushState;
    window.history.replaceState = replaceState;
  }

  function onWorkspaceNavigation(event) {
    syncContributionRoots(event.detail ?? {});
  }

  function onPageShow(event) {
    if (!event.persisted || invalidated || closed) return;
    checkSession();
    setDocumentHold('bfcache', false);
  }

  function onVisibilityChange() {
    if (closed || invalidated) return;
    if (document.visibilityState === 'hidden') setDocumentHold('hidden', true);
    else if (document.visibilityState === 'visible') {
      checkSession();
      setDocumentHold('hidden', false);
    }
  }

  function onPageHide(event) {
    cancelNavigation();
    checkVersion++;
    pendingCheck = null;
    if (event.persisted) {
      setDocumentHold('bfcache', true);
      return;
    }
    closed = true;
    const errors = [];
    terminate(Array.from(records.values()), 'document-closed', true, errors);
    records.clear();
    window.removeEventListener('pagehide', onPageHide);
    window.removeEventListener('pageshow', onPageShow);
    window.removeEventListener('focus', checkSession);
    window.removeEventListener('extension-workspace-invalidated', invalidate);
    window.removeEventListener('extension-workspace-will-navigate', prepareContributionRoots);
    window.removeEventListener('extension-workspace-navigation', onWorkspaceNavigation);
    document.removeEventListener('visibilitychange', onVisibilityChange);
    if (workspaceEnabled) {
      setNativeNavigator(null);
      document.removeEventListener('click', onWorkspaceClick);
      document.removeEventListener('submit', onWorkspaceSubmit);
      window.removeEventListener('popstate', onPopState);
      if (window.history.pushState === pushState) window.history.pushState = originalPushState;
      if (window.history.replaceState === replaceState) window.history.replaceState = originalReplaceState;
    }
    documentControllers.delete(document);
    report(errors);
  }

  window.addEventListener('pagehide', onPageHide);
  window.addEventListener('pageshow', onPageShow);
  window.addEventListener('focus', checkSession);
  window.addEventListener('extension-workspace-invalidated', invalidate);
  window.addEventListener('extension-workspace-will-navigate', prepareContributionRoots);
  window.addEventListener('extension-workspace-navigation', onWorkspaceNavigation);
  document.addEventListener('visibilitychange', onVisibilityChange);
  if (document.visibilityState === 'hidden') documentHolds.add('hidden');

  return {
    enablePages(nextImporter, fetcher) {
      if (pagesEnabled || closed) return;
      pagesEnabled = true;
      importer = nextImporter;
      pagesFetcher = fetcher;
      if (!generation || !validAppSubURL(admitted.dataset.appSubUrl)) {
        invalidate();
        return;
      }
      for (const root of document.querySelectorAll('[data-extension-panel]')) {
        const key = ownerKey(root);
        if (key) panelOwnerKeys.add(key);
      }
      for (const root of document.querySelectorAll('[data-extension-page], [data-extension-panel]')) mountRoot(root);
    },
    enableWorkspace(fetcher) {
      if (workspaceEnabled || closed) return;
      workspaceEnabled = true;
      workspaceFetcher = fetcher;
      if (invalidated || !generation || appSubPath === null) {
        invalidate();
        return;
      }
      enableHistory();
      syncPanelsHome(home);
      setNativeNavigator((url, {replace}) => {
        if (closed || invalidated) return;
        const decision = classifyWorkspaceNavigation(url, window.location.href, origin, appSubPath);
        if (decision.kind === 'native') transplant(intentFor(decision, 'programmatic', replace ? 'replace' : 'push'));
        else if (decision.kind === 'top') {
          cancelNavigation();
          fullLoad(intentFor(decision, 'programmatic', replace ? 'replace' : 'push'));
        }
      });
      document.addEventListener('click', onWorkspaceClick);
      document.addEventListener('submit', onWorkspaceSubmit);
      window.addEventListener('popstate', onPopState);
      syncConcealment();
    },
  };
}
