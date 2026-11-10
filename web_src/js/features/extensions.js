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

export function initExtensionPages(importer = importExtension, fetcher = (...args) => window.fetch(...args)) {
  const home = document.querySelector('[data-extension-panels]');
  const roots = document.querySelectorAll('[data-extension-page], [data-extension-panel]');
  // A page-only manifest leaves the eligible home empty on native boot;
  // the host still listens so transplanted pages mount on entry.
  if (!roots.length && !home) return;
  const pageRoots = new Set(Array.from(roots).filter((root) => root.hasAttribute('data-extension-page')));
  // NFX-C04/H-OWNER: the host selects one retained instance per declared
  // shared owner. The linked panel root is that instance; a page root
  // for the same (extension, owner) is a presentation slot only and
  // never mounts a competing owner.
  const panelOwnerKeys = new Set(Array.from(roots).filter((root) => root.hasAttribute('data-extension-panel')).map(ownerKey).filter(Boolean));
  const mounted = [];
  const pending = new Map();
  let leaving = false;
  const settle = (owner) => {
    if (!owner) return;
    if (leaving) {
      owner.dispose();
      return;
    }
    mounted.push(owner);
    // Initial presentation: a panel mounted while the persistent home is
    // hidden (native-first load) starts non-visible, so the host tells it
    // immediately instead of leaving it to assume visible work until the
    // first navigation update. A shared owner mounted visible on one of
    // its direct paths presents in page mode (NFX-A04/H-C), so it is told
    // at once as well. Other visible mounts need no initial update: the
    // contract default already matches the host state.
    if (!owner.root.hasAttribute('data-extension-page')) {
      const route = `${window.location.pathname}${window.location.search}${window.location.hash}`;
      const repositoryId = owner.root.dataset.extensionRepositoryId;
      const facts = {
        route, params: Object.fromEntries(new URLSearchParams(window.location.search)),
        ...(repositoryId ? {repositoryId} : {}),
      };
      if (home?.hasAttribute('hidden')) {
        owner.update({...facts, mode: 'panel', visible: false});
      } else if (ownerDirectPath(window.location.pathname, owner.root)) {
        owner.update({...facts, mode: 'page', visible: true});
      }
    }
  };
  function mountRoot(root) {
    if (leaving || root.hasAttribute('data-extension-mounted')) return;
    // Shared workspace owners can create inventory reads and live sessions.
    // Do not import or initialize them until their presentation is opened.
    if (root.hasAttribute('data-extension-panel') && root.dataset.extensionOwnerId && home?.hasAttribute('hidden')) return;
    if (root.hasAttribute('data-extension-page') && panelOwnerKeys.has(ownerKey(root))) {
      root.setAttribute('data-extension-owner-presented', '');
      return;
    }
    root.setAttribute('data-extension-mounted', '');
    const controller = new AbortController();
    pending.set(controller, root);
    mountContribution(root, {importer, fetcher, signal: controller.signal}).then((owner) => {
      pending.delete(controller);
      settle(owner);
    });
  }
  for (const root of roots) mountRoot(root);
  // region_adopt: page roots persist across plain legs. Before the content
  // swap the workspace parks live page roots into a hidden shelter inside
  // the persistent panels home; the post-swap sync readopts them into the
  // entered duplicate (same identity reuses the owner, streams suspended
  // meanwhile). The shelter holds at most four owners; the oldest parked
  // root past four disposes, unless it carries data-extension-persist: a
  // persistent shared owner (the Workspace terminal once C04 joins it) is
  // never evicted silently, so the bound yields when every parked root
  // persists.
  let park = home?.querySelector('[data-extension-page-park]') ?? null;
  function parkPageRoots() {
    if (leaving) return;
    if (!park) {
      park = document.createElement('div');
      park.setAttribute('data-extension-page-park', '');
      park.setAttribute('hidden', '');
      (home ?? document.body).append(park);
    }
    for (const owner of mounted) {
      if (!pageRoots.has(owner.root) || !owner.root.isConnected || park.contains(owner.root)) continue;
      owner.suspend();
      park.append(owner.root);
    }
    while (park.children.length > 4) {
      const oldest = Array.from(park.children).find((root) => !root.hasAttribute('data-extension-persist'));
      if (!oldest) return;
      const owner = mounted.find((candidate) => candidate.root === oldest);
      oldest.remove();
      if (owner) {
        mounted.splice(mounted.indexOf(owner), 1);
        pageRoots.delete(owner.root);
        owner.dispose();
      }
    }
  }
  function reconcilePanels(enteredHome) {
    if (!home || leaving) return;
    const entered = new Map();
    for (const root of enteredHome.querySelectorAll('[data-extension-panel]')) {
      const parsed = parseMountDataset(root);
      if (!parsed.ok || parsed.sessionGeneration !== home.dataset.extensionSessionGeneration ||
          appSubURLPathname(parsed.appSubUrl) !== appSubURLPathname(home.dataset.appSubUrl)) continue;
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
      // Withdrawn admissions retire even when the session generation is
      // unchanged. Abort a pending mount before it can publish private UI.
      for (const [controller, pendingRoot] of pending) {
        if (pendingRoot !== root) continue;
        controller.abort();
        pending.delete(controller);
      }
      const owner = mounted.find((candidate) => candidate.root === root);
      root.setAttribute('hidden', '');
      root.setAttribute('inert', '');
      const section = root.closest('.extension-workspace-panel');
      (section?.parentElement === home ? section : root).remove();
      if (owner) {
        mounted.splice(mounted.indexOf(owner), 1);
        try {
          owner.retire('authority-revoked');
        } catch (error) {
          console.error('Could not retire withdrawn contribution', error);
        }
      }
    }
    for (const root of entered.values()) {
      const section = root.closest('.extension-workspace-panel');
      home.append(section?.parentElement === enteredHome ? section : root);
      mountRoot(root);
    }
  }
  function prepareContributionRoots(event) {
    // Only a fetched native document carries fresh panel admission facts.
    // Hash-only moves and BFCache session checks do not revoke declarations.
    if (event.detail?.panelsHome) reconcilePanels(event.detail.panelsHome);
    parkPageRoots();
  }
  function abortPending() {
    for (const controller of pending.keys()) controller.abort();
    pending.clear();
  }
  function disposeRoots() {
    if (leaving) return;
    leaving = true;
    abortPending();
    for (const owner of mounted.splice(0)) owner.dispose();
    window.removeEventListener('extension-workspace-invalidated', retireRoots);
    window.removeEventListener('extension-workspace-will-navigate', prepareContributionRoots);
    window.removeEventListener('extension-workspace-navigation', onWorkspaceNavigation);
  }
  function retireRoots() {
    if (leaving) return;
    leaving = true;
    abortPending();
    for (const owner of mounted.splice(0)) owner.retire('session-changed');
    window.removeEventListener('extension-workspace-invalidated', retireRoots);
    window.removeEventListener('extension-workspace-will-navigate', prepareContributionRoots);
    window.removeEventListener('extension-workspace-navigation', onWorkspaceNavigation);
  }
  // Each transplant swaps the native content region. Parked page owners
  // whose contribution persists readopt into the entered duplicate
  // (identity triple plus generation must match; the entered duplicate is
  // discarded unmounted), receive the ordered update, and resume; parked
  // owners with no matching root stay suspended in the shelter but still
  // receive current facts as visible:false. Panels report the home's live
  // visibility. Owners whose root left the document without parking
  // dispose (pages) or retire (panels); genuinely new roots mount.
  // Adoption runs after native init: native features target native
  // content, never contribution roots, so init effects on the entered
  // duplicate are discarded with it while the adopted owner keeps state.
  function syncContributionRoots(facts) {
    if (leaving) return;
    for (const [controller, root] of Array.from(pending)) {
      if (!root.isConnected || root.hasAttribute('data-extension-page')) {
        controller.abort();
        pending.delete(controller);
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
      const loose = mountIdentity(parsed, {includeContractVersion: false});
      if (identity) {
        if (!entered.has(identity)) entered.set(identity, root);
        else if (!root.hasAttribute('data-extension-mounted')) mountRoot(root);
      } else {
        refused.push(root);
      }
      if (loose && !enteredLoose.has(loose)) enteredLoose.set(loose, root);
      const slot = contributionSlot(parsed);
      if (slot) enteredSlots.set(slot, root);
    }
    const panelsVisible = home ? !home.hasAttribute('hidden') : true;
    const owned = new Set();
    for (const owner of Array.from(mounted)) {
      const isPage = pageRoots.has(owner.root);
      if (isPage && park !== null && park.contains(owner.root)) {
        const duplicate = entered.get(rootIdentity(owner.root));
        if (duplicate && duplicate !== owner.root) {
          entered.delete(rootIdentity(owner.root));
          duplicate.replaceWith(owner.root);
          refreshMountPresentation(owner.root, duplicate);
          owned.add(owner.root);
          owner.update({...facts, mode: 'page', visible: true});
          owner.resume({sessionGeneration: owner.root.dataset.extensionSessionGeneration});
          continue;
        }
        // Version skew: an otherwise identical entered root whose
        // contract version is missing or differs is never adopted and
        // never mounted; the parked owner retires and both roots leave
        // the document. Checked before any adoption or second mount.
        const skewed = enteredLoose.get(rootIdentity(owner.root, {includeContractVersion: false}));
        if (skewed && skewed !== owner.root) {
          entered.delete(rootIdentity(skewed));
          entered.delete(rootIdentity(owner.root));
          skewed.remove();
          owner.root.remove();
          mounted.splice(mounted.indexOf(owner), 1);
          pageRoots.delete(owner.root);
          owner.retire('contribution-unavailable');
          continue;
        }
        if (enteredSlots.has(contributionSlot(parseMountDataset(owner.root)))) {
          owner.root.remove();
          mounted.splice(mounted.indexOf(owner), 1);
          pageRoots.delete(owner.root);
          owner.retire('contribution-unavailable');
          continue;
        }
        owner.update({...facts, mode: 'page', visible: false});
        continue;
      }
      if (!owner.root.isConnected) {
        mounted.splice(mounted.indexOf(owner), 1);
        if (isPage) {
          pageRoots.delete(owner.root);
          owner.dispose();
        } else {
          owner.retire('contribution-unavailable');
        }
        continue;
      }
      owned.add(owner.root);
      // A linked shared owner's retained panel presents in page mode on
      // its owner's direct paths (NFX-C04/H-OWNER).
      const routePath = typeof facts.route === 'string' ? facts.route.split(/[?#]/)[0] : '';
      const ownerDirect = !isPage && panelsVisible && ownerDirectPath(routePath, owner.root);
      owner.update({...facts, mode: isPage || ownerDirect ? 'page' : 'panel', visible: isPage ? true : panelsVisible});
    }
    for (const root of entered.values()) {
      if (owned.has(root)) continue;
      pageRoots.add(root);
      mountRoot(root);
    }
    for (const root of refused) {
      if (root.isConnected) mountRoot(root);
    }
    if (panelsVisible) {
      for (const root of home?.querySelectorAll('[data-extension-panel]') ?? []) mountRoot(root);
    }
  }
  function onWorkspaceNavigation(event) {
    syncContributionRoots(event.detail ?? {});
  }
  window.addEventListener('pagehide', (event) => { if (!event.persisted) disposeRoots(); });
  window.addEventListener('extension-workspace-invalidated', retireRoots);
  window.addEventListener('extension-workspace-will-navigate', prepareContributionRoots);
  window.addEventListener('extension-workspace-navigation', onWorkspaceNavigation);
  if (!pageRoots.size && !home) return;
  const firstPage = pageRoots.size ? pageRoots.values().next().value : null;
  const generation = firstPage?.dataset.extensionSessionGeneration ?? home?.dataset.extensionSessionGeneration;
  const appSubURL = firstPage?.dataset.appSubUrl ?? home?.dataset.appSubUrl;
  const checkURL = new URL(sessionCheckPath(appSubURL), window.location.origin).href;
  let checkVersion = 0;
  let pendingCheck;
  let invalidated = false;
  // Revalidation conceals the whole retained region at once: every live
  // page root plus the persistent home (which carries all panel roots and
  // the parked shelter). Display alone toggles, so connected DOM and
  // sockets survive a valid session; only a mismatch retires owners and
  // posts the notice.
  const concealTargets = () => {
    const targets = Array.from(pageRoots);
    if (home && !targets.includes(home)) targets.push(home);
    return targets;
  };
  const invalidate = () => {
    if (invalidated) return;
    invalidated = true;
    for (const root of concealTargets()) {
      root.replaceChildren();
      root.style.display = '';
      root.textContent = 'Your session changed. Reload Forgejo to continue.';
    }
    retireRoots();
  };
  const checkSession = () => {
    if (invalidated || pendingCheck) return;
    // One joined lifecycle: conceal and suspend retained private state
    // before revalidation, and resume it only after the live session
    // verifies against the admitted generation. Consumers never
    // interpret cache/visibility events themselves.
    for (const root of concealTargets()) root.style.display = 'none';
    for (const owner of mounted) owner.suspend();
    const version = ++checkVersion;
    pendingCheck = liveSessionMatches(checkURL, generation, fetcher).then((matches) => {
      if (version !== checkVersion) return;
      if (!matches) invalidate();
      else {
        for (const root of concealTargets()) root.style.display = '';
        for (const owner of mounted) owner.resume({sessionGeneration: generation});
      }
    }).finally(() => { if (version === checkVersion) pendingCheck = null; });
  };
  const onPageShow = (event) => { if (event.persisted) checkSession(); };
  const onVisibilityChange = () => {
    if (document.visibilityState === 'hidden') {
      for (const owner of mounted) owner.suspend();
      return;
    }
    if (document.visibilityState === 'visible') checkSession();
  };
  const onPageHide = (event) => {
    checkVersion++;
    pendingCheck = null;
    for (const root of concealTargets()) root.style.display = 'none';
    if (event.persisted) {
      for (const owner of mounted) owner.suspend();
      return;
    }
    window.removeEventListener('pagehide', onPageHide);
    window.removeEventListener('pageshow', onPageShow);
    window.removeEventListener('focus', checkSession);
    document.removeEventListener('visibilitychange', onVisibilityChange);
  };
  window.addEventListener('pagehide', onPageHide);
  window.addEventListener('pageshow', onPageShow);
  window.addEventListener('focus', checkSession);
  document.addEventListener('visibilitychange', onVisibilityChange);
  if (!generation || !validAppSubURL(appSubURL)) invalidate();
}

export function initExtensionWorkspace(fetcher = (...args) => window.fetch(...args)) {
  // region_adopt: every eligible document is a whole native document with
  // the persistent panels home in the shared layout. Transplants swap the
  // native content region; the home (and its panel roots) never moves.
  const home = document.querySelector('[data-extension-panels]');
  if (!home) return;
  const liveContent = () => document.querySelector('.page-content');
  let navigationVersion = 0;
  let navigationController = null;
  const base = window.location.href;
  const origin = window.location.origin;
  const appSubPath = home.dataset.appSubUrl;
  const generation = home.dataset.extensionSessionGeneration;
  let invalidated = false;
  let pendingCheck;
  let checkVersion = 0;

  function invalidate() {
    if (invalidated) return;
    invalidated = true;
    navigationVersion++;
    navigationController?.abort();
    home.setAttribute('hidden', '');
    home.setAttribute('inert', '');
    const content = liveContent();
    if (content) content.style.display = 'none';
    try {
      retirePageFeatures();
    } catch (error) {
      console.error('Could not retire native page features', error);
    } finally {
      setPageConfig({});
      if (content) {
        content.replaceChildren();
        content.textContent = 'Your session changed. Reload Forgejo to continue.';
        content.style.display = '';
      }
      window.dispatchEvent(new Event('extension-workspace-invalidated'));
    }
  }

  function checkSession() {
    if (invalidated || pendingCheck) return pendingCheck;
    const version = ++checkVersion;
    // Keep native private content hidden until the live session verifies;
    // the contribution lifecycle above suspends/resumes/retires owners
    // against the same admitted generation.
    const content = liveContent();
    if (content) content.style.display = 'none';
    const endpoint = new URL(sessionCheckPath(appSubPath), origin);
    pendingCheck = liveSessionMatches(endpoint.href, generation, fetcher)
      .then((matches) => {
        if (version !== checkVersion) return;
        if (!matches) {
          invalidate();
        } else if (content) {
          content.style.display = '';
        }
      })
      .finally(() => {
        if (version === checkVersion) pendingCheck = null;
      });
    return pendingCheck;
  }

  if (!generation || !validAppSubURL(appSubPath)) {
    invalidate();
    return;
  }
  // A cached document rechecks the current native session on return.
  const onPageShow = (event) => { if (event.persisted) checkSession(); };
  const onVisibilityChange = () => { if (document.visibilityState === 'visible') checkSession(); };
  window.addEventListener('pageshow', onPageShow);
  window.addEventListener('focus', checkSession);
  document.addEventListener('visibilitychange', onVisibilityChange);
  // The live document is the current route; unknown layouts stay fully
  // native (transplant full-loads them) until their features migrate.
  let current = `${window.location.pathname}${window.location.search}${window.location.hash}`;
  // Refused traversals restore the original history position with a
  // compensating move, never pushState (which truncates forward entries).
  // Host-created entries carry an index in their state; the landed index
  // gives the signed traversal step. Foreign entries without an index fall
  // back to restoring the address in place.
  const INDEX_KEY = '__workspaceNavIndex';
  let restoring = false;
  let currentIndex = typeof window.history.state?.[INDEX_KEY] === 'number' ? window.history.state[INDEX_KEY] : 0;
  const nativePushState = window.history.pushState.bind(window.history);
  const nativeReplaceState = window.history.replaceState.bind(window.history);
  try {
    nativeReplaceState({...window.history.state, [INDEX_KEY]: currentIndex}, '', window.location.href);
  } catch {}
  // Native features own same-document history writes (code line selections
  // push entries; the commit graph rewrites its query in place). The host
  // coordinates them here instead of letting them create unindexed entries
  // or wipe the entry state: pushes take the next host index, replacements
  // keep the current one, and the tracked route follows the real address.
  const trackNativeWrite = (url) => {
    if (url === null || url === undefined) return;
    try {
      const next = new URL(url, window.location.href);
      if (next.origin === origin) current = `${next.pathname}${next.search}${next.hash}`;
    } catch {}
  };
  window.history.pushState = (state, title, url) => {
    const stamped = {...(state && typeof state === 'object' ? state : {}), scrollY: 0, [INDEX_KEY]: currentIndex + 1};
    nativePushState(stamped, title, url);
    currentIndex++;
    trackNativeWrite(url);
  };
  window.history.replaceState = (state, title, url) => {
    const stamped = {...(state && typeof state === 'object' ? state : {}), [INDEX_KEY]: currentIndex};
    nativeReplaceState(stamped, title, url);
    trackNativeWrite(url);
  };

  // The persistent home is visible on direct contribution paths and
  // hidden+inert on ordinary native shapes; the server renders the initial
  // state and each transplant resyncs it. Hiding never drops focus to the
  // body: focus inside the home moves to the entered content region.
  // Showing never steals focus.
  function syncPanelsHome(enteredHome) {
    // The admitted contribution decides whether its page uses a retained
    // panel. A URL suffix cannot distinguish that page from a separate feed.
    if (!enteredHome.hasAttribute('hidden')) {
      home.removeAttribute('hidden');
      home.removeAttribute('inert');
      return;
    }
    const focusInside = home.contains(document.activeElement);
    home.setAttribute('hidden', '');
    home.setAttribute('inert', '');
    if (focusInside) {
      const live = liveContent();
      if (live) {
        live.setAttribute('tabindex', '-1');
        live.focus({preventScroll: true});
      }
    }
  }
  syncPanelsHome(home);
  // Public programmatic navigation (NFX-C04/H-ROUTE): contributions and
  // native features request transitions through the host instead of
  // assigning window.location. Admitted native paths transplant;
  // host-handled destinations take their native full load; private
  // native prefixes are refused.
  setNativeNavigator((url, {replace}) => {
    const decision = classifyWorkspaceNavigation(url, window.location.href, origin, appSubPath);
    if (decision.kind === 'native') transplant(decision.path, !replace);
    else if (decision.kind === 'top') window.location.assign(decision.url);
  });

  // Active link states are per-page chrome. States copy across matching
  // links so listeners on the live navbar survive the transition.
  function syncNavbarStates(native) {
    const live = document.getElementById('navbar');
    const incoming = native.getElementById('navbar');
    if (!live || !incoming) return;
    const incomingByHref = new Map();
    for (const link of incoming.querySelectorAll('a[href]')) {
      incomingByHref.set(new URL(link.getAttribute('href'), origin).href, link);
    }
    for (const link of live.querySelectorAll('a[href]')) {
      const peer = incomingByHref.get(new URL(link.getAttribute('href'), origin).href);
      link.classList.toggle('active', Boolean(peer?.classList.contains('active')));
      if (peer?.hasAttribute('aria-current')) link.setAttribute('aria-current', peer.getAttribute('aria-current'));
      else link.removeAttribute('aria-current');
    }
  }

  async function transplant(path, push = false, traversal = false, excludeForm = null, traversalStep = 0, viaSubmit = false) {
    const target = new URL(path, origin);
    const previous = new URL(current, origin);
    navigationController?.abort();
    // Already-current URL (same path, query and fragment, including repeat
    // fragment clicks): native anchor behaviour only. No dirty prompt, no
    // fetch, no history entry; the draft and page objects stay untouched.
    // A native GET submission is not an anchor click: repeating an
    // unchanged query must still perform its native GET/refresh.
    if (!viaSubmit && target.href === previous.href) {
      if (target.hash) focusFragment(target.hash);
      return true;
    }
    if (target.pathname === previous.pathname && target.search === previous.search && target.hash !== previous.hash) {
      navigationVersion++;
      if (push) {
        nativeReplaceState({...window.history.state, scrollY: window.scrollY, [INDEX_KEY]: currentIndex}, '', window.location.href);
        nativePushState({...window.history.state, scrollY: 0, [INDEX_KEY]: currentIndex + 1}, '', path);
        currentIndex++;
      } else updateHistory(path);
      current = path;
      if (target.hash) focusFragment(target.hash);
      if (!traversal) window.dispatchEvent(new HashChangeEvent('hashchange', {oldURL: previous.href, newURL: target.href}));
      home.dispatchEvent(new CustomEvent('extension-workspace-navigation', {bubbles: true, detail: {
        route: path, params: Object.fromEntries(target.searchParams),
        ...(home.dataset.extensionRepositoryId ? {repositoryId: home.dataset.extensionRepositoryId} : {}),
      }}));
      return true;
    }
    if (hasDirtyNativeForm(excludeForm)) {
      let confirmed = false;
      try {
        confirmed = window.confirm?.('You have unsaved changes!') ?? false;
      } catch {
        confirmed = false;
      }
      if (!confirmed) {
        if (traversal) restoreTraversalPosition(traversalStep);
        return false;
      }
    }
    navigationController = new AbortController();
    const version = ++navigationVersion;
    const signal = navigationController.signal;
    let timedOut = false;
    const timer = setTimeout(() => {
      timedOut = true;
      navigationController.abort();
    }, 15000);
    let response;
    try {
      response = await fetcher(new URL(path, origin).href, {
        credentials: 'same-origin', cache: 'no-store', redirect: 'error',
        headers: {Accept: 'text/html', 'X-Extension-Workspace-Navigation': '1'}, signal,
      });
    } catch {
      clearTimeout(timer);
      if (!invalidated && version === navigationVersion && (timedOut || !signal.aborted)) window.location.assign(path);
      return;
    }
    clearTimeout(timer);
    if (invalidated || version !== navigationVersion || signal.aborted) return;
    if (!response.ok) {
      window.location.assign(path);
      return;
    }
    // P1: verify the entered document belongs to the live session before
    // touching any retained state. A foreign generation means the cookie or
    // actor changed out from under the shell: retire, never join.
    if (response.headers.get('X-Extension-Session-Generation') !== generation) {
      invalidate();
      return;
    }
    let html;
    try {
      html = await response.text();
    } catch {
      if (!invalidated && version === navigationVersion) window.location.assign(path);
      return;
    }
    if (invalidated || version !== navigationVersion || signal.aborted) return;
    const native = new DOMParser().parseFromString(html, 'text/html');
    const live = liveContent();
    const content = native.querySelector('.page-content');
    const enteredHome = native.querySelector('[data-extension-panels]');
    const pageData = readNativePageData(native);
    if (!live || !content || !pageData || !enteredHome) {
      window.location.assign(path);
      return;
    }
    if (enteredHome.dataset.extensionSessionGeneration !== generation) {
      invalidate();
      return;
    }
    if (invalidated || version !== navigationVersion || signal.aborted) return;
    // Only Fountain's native content region is replaced. The persistent
    // panels home, parked page roots and terminal DOM never leave the
    // owning document.
    const repositoryId = enteredHome.dataset.extensionRepositoryId ?? content.querySelector('[data-extension-page]')?.dataset.extensionRepositoryId ?? '';
    // Snapshot outgoing scroll before retiring/swapping: replacing tall
    // content can clamp window.scrollY, so reading it afterwards loses
    // the position Back must restore.
    const outgoingScrollY = window.scrollY;
    const focusInContent = live.contains(document.activeElement);
    try {
      // Park live page roots before the swap so owners persist the leg.
      window.dispatchEvent(new CustomEvent('extension-workspace-will-navigate', {detail: {panelsHome: enteredHome}}));
      retirePageFeatures();
      setPageConfig(pageData);
      document.body.dataset.pageData = JSON.stringify(pageData);
      document.title = native.title;
      current = path;
      if (push) {
        nativeReplaceState({...window.history.state, scrollY: outgoingScrollY, [INDEX_KEY]: currentIndex}, '', window.location.href);
        nativePushState({...window.history.state, scrollY: 0, [INDEX_KEY]: currentIndex + 1}, '', path);
        currentIndex++;
      } else updateHistory(path);
      // Native custom elements also observe entered facts on connection.
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
      // Initializers read the entered address as well as its page data.
      initPageFeatures();
    } catch {
      window.location.assign(path);
      return;
    }
    if (push) {
      if (target.hash) focusFragment(target.hash);
      else window.scrollTo(0, 0);
    }
    home.dispatchEvent(new CustomEvent('extension-workspace-navigation', {bubbles: true, detail: {
      route: `${target.pathname}${target.search}${target.hash}`,
      params: Object.fromEntries(target.searchParams),
      ...(repositoryId ? {repositoryId} : {}),
    }}));
  }

  function onWorkspaceClick(event) {
    if (event.defaultPrevented || event.button !== 0 || event.metaKey || event.ctrlKey || event.shiftKey || event.altKey) return;
    const link = event.composedPath().find((node) => node?.localName === 'a' && 'href' in node);
    if (!link || link.hasAttribute('download') || (link.target && link.target !== '_self')) return;
    const decision = classifyWorkspaceNavigation(link.href, window.location.href, origin, appSubPath);
    if (decision.kind === 'reject') {
      event.preventDefault();
      return;
    }
    if (decision.kind !== 'native') return;
    event.preventDefault();
    transplant(decision.path, true);
  }
  document.addEventListener('click', onWorkspaceClick);

  // GET forms navigate like links: same-origin GET submits to transplantable
  // paths swap the content region. formFetchAction runs first (index.js order) and
  // claims .form-fetch-action forms; POST and targeted forms keep native load.
  function onWorkspaceSubmit(event) {
    if (event.defaultPrevented) return;
    const form = event.target;
    if (!(form instanceof HTMLFormElement)) return;
    if (form.target && form.target !== '_self') return;
    const submitter = submitEventSubmitter(event);
    const effectiveMethod = (submitter?.getAttribute('formmethod') || form.getAttribute('method') || 'get').toLowerCase();
    if (effectiveMethod !== 'get') return;
    const formData = new FormData(form);
    const submitterName = submitter?.getAttribute('name');
    if (submitterName) formData.append(submitterName, submitter.getAttribute('value') || '');
    const params = new URLSearchParams();
    for (const [key, value] of formData) params.append(key, value.toString());
    const target = new URL(submitter?.getAttribute('formaction') || form.getAttribute('action') || window.location.href, window.location.href);
    target.search = params.toString();
    const decision = classifyWorkspaceNavigation(target.href, window.location.href, origin, appSubPath);
    if (decision.kind !== 'native') return;
    event.preventDefault();
    transplant(decision.path, true, false, form, 0, true);
  }
  document.addEventListener('submit', onWorkspaceSubmit);

  function updateHistory(path) {
    const state = window.history.state;
    // Traversal already landed on this URL; rewriting the entry is a no-op
    // there and must not touch the surrounding stack positions.
    if (new URL(path, origin).href !== window.location.href) nativeReplaceState(state, '', path);
    if (typeof state?.[INDEX_KEY] === 'number') currentIndex = state[INDEX_KEY];
  }

  // Move back to the entry a refused traversal came from. The compensating
  // move fires one popstate, which onPopState consumes without transplanting.
  function restoreTraversalPosition(step) {
    if (step) {
      restoring = true;
      try {
        window.history.go(-step);
        return;
      } catch {
        restoring = false;
      }
    }
    try {
      nativeReplaceState(window.history.state, '', current);
    } catch {}
  }

  const onPopState = () => {
    if (invalidated) return;
    if (restoring) {
      restoring = false;
      if (typeof window.history.state?.[INDEX_KEY] === 'number') currentIndex = window.history.state[INDEX_KEY];
      return;
    }
    navigationVersion++;
    const landedIndex = window.history.state?.[INDEX_KEY];
    const step = typeof landedIndex === 'number' ? landedIndex - currentIndex : 0;
    const path = workspacePath(`${window.location.pathname}${window.location.search}${window.location.hash}`, base, origin, appSubPath);
    if (!path || path === current) {
      if (typeof landedIndex === 'number') currentIndex = landedIndex;
      return;
    }
    transplant(path, false, true, null, step).then((proceeded) => {
      if (proceeded !== false && !invalidated) window.scrollTo(0, window.history.state?.scrollY ?? 0);
    });
  };
  window.addEventListener('popstate', onPopState);
  const onPageHide = (event) => {
    navigationVersion++;
    navigationController?.abort();
    checkVersion++;
    pendingCheck = null;
    if (event.persisted) {
      // Cached native content stays hidden until revalidation verifies it.
      const content = liveContent();
      if (content) content.style.display = 'none';
      return;
    }
    setNativeNavigator(null);
    document.removeEventListener('click', onWorkspaceClick);
    document.removeEventListener('submit', onWorkspaceSubmit);
    window.removeEventListener('pagehide', onPageHide);
    window.removeEventListener('pageshow', onPageShow);
    window.removeEventListener('focus', checkSession);
    window.removeEventListener('popstate', onPopState);
    document.removeEventListener('visibilitychange', onVisibilityChange);
  };
  window.addEventListener('pagehide', onPageHide);
  updateHistory(current);
}
