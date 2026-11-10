// Retained contribution owner for the native extension host (NFX-C02/H-0).
//
// C-owned implementation; A02 integrates it into the native transition seam.
// This module owns mount validation, the contribution lifecycle
// (update/suspend/resume/retire) and scoped transport. A's presentation layer
// owns root visibility/text and the native transition; B's lifecycle hooks own
// native feature init/teardown.
//
// Lifetime guarantees (see sdk/browser/extension-contract.d.ts): update() and
// suspend()/resume() always preserve the exact root and all retained resources
// (renderer/socket where used). Only retire() — authority loss or removal —
// closes contribution streams. The visible retained owner receives update()
// across ordinary navigation; a sheltered owner suspends and resumes with
// the same root, resources and stream.

import {navigateNative} from '../native-lifecycle.js';
import {appSubURLPathname, validDirectPattern} from './extension-navigation-policy.js';

const mountSlug = /^[a-z0-9]+(?:-[a-z0-9]+)*$/;

function nativeMountURL(input, appPathname) {
  if (typeof input !== 'string') return null;
  try {
    const url = new URL(input, window.location.href);
    return url.origin === window.location.origin && !url.username && !url.password && !url.search && !url.hash &&
      !/%2f|%5c/i.test(url.pathname) && url.pathname.startsWith(`${appPathname}/`) ? url : null;
  } catch {
    return null;
  }
}

function nativeAPIPath(pathname, appSubURL, suffix) {
  if (!pathname.endsWith(suffix)) return false;
  const prefix = pathname.slice(0, -suffix.length);
  if (!prefix.startsWith(appSubURL)) return false;
  const scope = prefix.slice(appSubURL.length);
  return scope === '' || scope === '/user/settings' || scope === '/admin' || /^\/[^/]+\/[^/]+$/.test(scope);
}

function ownerPageAPIPath(pathname, appSubURL, extensionId) {
  const segments = pathname.slice(appSubURL.length).split('/');
  return segments.length === 8 && segments[0] === '' && segments[1] === '-' &&
    segments[2] === 'native' && segments[3] === 'pages' && segments[4] === extensionId &&
    mountSlug.test(segments[5]) && segments[6] === 'api' && segments[7] === '';
}

function mountDirectPatterns(value) {
  if (value === undefined) return [];
  try {
    const patterns = JSON.parse(value);
    return Array.isArray(patterns) && patterns.every(validDirectPattern) ? patterns : null;
  } catch {
    return null;
  }
}

function scopedTarget(relativePath, apiHref) {
  const target = new URL(relativePath, apiHref);
  if (target.origin !== window.location.origin || target.username || target.password ||
      !target.href.startsWith(apiHref) || /%2f|%5c/i.test(target.pathname)) {
    return null;
  }
  return target;
}

/** Canonical browser contract version implemented by this mount module. Must match the host-rendered data-extension-contract-version. */
const browserContractVersion = 1;

/** Validate a contribution root's dataset without importing or fetching. */
export function parseMountDataset(root) {
  const {appSubUrl, extensionEntry, extensionId, extensionPageId, extensionPanelId, extensionOwnerId, extensionApiBase, extensionAssetBase, extensionSessionGeneration, extensionRepositoryId, extensionContractVersion, extensionDirectPatterns} = root.dataset;
  const mode = extensionPageId ? 'page' : 'panel';
  const contributionId = extensionPageId || extensionPanelId;
  if (extensionPageId && extensionPanelId) return {ok: false};
  if (!mountSlug.test(extensionId ?? '') || !mountSlug.test(contributionId ?? '') || !extensionSessionGeneration) return {ok: false};
  if (extensionOwnerId !== undefined && !mountSlug.test(extensionOwnerId)) return {ok: false};
  const appPathname = appSubURLPathname(appSubUrl);
  if (appPathname === null) return {ok: false};
  const directPatterns = mountDirectPatterns(extensionDirectPatterns);
  if (directPatterns === null) return {ok: false};
  const entry = nativeMountURL(extensionEntry, appPathname);
  const assets = nativeMountURL(extensionAssetBase, appPathname);
  const api = nativeMountURL(extensionApiBase, appPathname);
  const apiSuffix = `/-/native/${mode}s/${extensionId}/${contributionId}/api/`;
  // A linked shared owner's panel carries the page's canonical API base
  // (NFX-C04/H-OWNER): one API identity for both modes.
  const ownerPageAPI = mode === 'panel' && Boolean(extensionOwnerId) && Boolean(api) &&
    ownerPageAPIPath(api.pathname, appPathname, extensionId);
  if (!entry || !assets || !api || assets.pathname !== `${appPathname}/-/native/assets/${extensionId}/` ||
      !entry.href.startsWith(assets.href) || (!nativeAPIPath(api.pathname, appPathname, apiSuffix) && !ownerPageAPI)) {
    return {ok: false};
  }
  return {
    ok: extensionContractVersion === String(browserContractVersion), mode, contributionId,
    entryHref: entry.href,
    apiHref: api.href,
    assetHref: assets.href,
    extensionId, appSubUrl,
    ownerId: extensionOwnerId,
    sessionGeneration: extensionSessionGeneration,
    repositoryId: extensionRepositoryId,
    contractVersion: extensionContractVersion,
    directPatterns,
  };
}

/** Identity of the admitted contribution definition, independent of display facts. */
export function mountIdentity(parsed, {includeContractVersion = true} = {}) {
  if (!parsed?.mode || includeContractVersion && !parsed.ok) return null;
  return JSON.stringify([
    parsed.mode, parsed.extensionId, parsed.contributionId, parsed.ownerId ?? '',
    parsed.entryHref, parsed.apiHref, parsed.assetHref, appSubURLPathname(parsed.appSubUrl),
    parsed.sessionGeneration, ...(includeContractVersion ? [parsed.contractVersion ?? ''] : []),
  ]);
}

/** Shared owners are private to one extension's owner namespace. */
export function mountOwnerKey(parsed) {
  return parsed?.ok && parsed.ownerId ? JSON.stringify([parsed.extensionId, parsed.ownerId]) : null;
}

/** Scoped fetch bound to the contribution apiBase and session generation. */
export function createScopedRequest(apiHref, generation, fetcher) {
  return (relativePath, options = {}) => {
    const target = scopedTarget(relativePath, apiHref);
    if (!target) throw new TypeError('Product API URL is invalid');
    const headers = new Headers(options.headers);
    headers.set('X-Extension-Session-Generation', generation);
    return fetcher(target.href, {...options, credentials: 'same-origin', redirect: 'error', cache: 'no-store', headers});
  };
}

/**
 * Scoped stream bound to the contribution apiBase. Returns the declared
 * ContributionStream handle ({socket, close}). The browser cannot set
 * handshake headers, so the server admits the stream from the live cookie
 * session; no query string or subprotocols are sent (the server rejects them).
 */
export function createScopedStream(apiHref, websocketClass) {
  if (typeof websocketClass !== 'function') throw new TypeError('Contribution streams are unavailable');
  const wsBase = new URL(apiHref);
  wsBase.protocol = wsBase.protocol === 'https:' ? 'wss:' : 'ws:';
  return (relativePath, options = {}) => {
    const target = scopedTarget(relativePath, apiHref);
    if (!target || target.search || target.hash) throw new TypeError('Product stream URL is invalid');
    const socket = new websocketClass(`${wsBase.protocol}//${target.host}${target.pathname}`);
    if (options.signal) {
      if (options.signal.aborted) socket.close();
      else {
        const abort = () => socket.close();
        options.signal.addEventListener('abort', abort, {once: true});
        socket.addEventListener?.('close', () => options.signal.removeEventListener('abort', abort), {once: true});
      }
    }
    return {socket, close: (code, reason) => socket.close(code, reason)};
  };
}

const defaultImporter = (url) => import(/* webpackIgnore: true */ url);

function normalizeHandle(result) {
  if (typeof result === 'function') return {dispose: result};
  if (result && typeof result === 'object') {
    const handle = {};
    for (const key of ['dispose', 'update', 'suspend', 'resume', 'retire']) {
      if (typeof result[key] === 'function') handle[key] = (...args) => result[key](...args);
    }
    return handle;
  }
  return {};
}

/**
 * Mount one retained contribution owner. Returns the owner handle, or null
 * when the root refuses to mount (no import or fetch is attempted then) or
 * when the host invalidates the mount while it is still pending.
 *
 * The host drives update/suspend/resume/retire after mount. Ordinary native
 * navigation updates the visible retained owner; a sheltered owner suspends
 * and resumes without dropping its resources or stream. Retirement follows
 * authority loss or contribution removal. Native page-feature teardown owns
 * native DOM only and never retires the admitted shared owner.
 *
 * Pass hooks.signal to let the host invalidate while the entry import or an
 * async mount is still pending: mount is never invoked after invalidation,
 * pending helpers are revoked, and tracked streams are closed even if the
 * mount never settles. Aborting after settle retires the owner.
 */
export async function mountContribution(root, hooks = {}) {
  const {importer = defaultImporter, fetcher = (...args) => window.fetch(...args), websocketClass = globalThis.WebSocket, navigator = navigateNative, signal} = hooks;
  const parsed = parseMountDataset(root);
  if (!parsed.ok) {
    root.textContent = 'This workspace view is unavailable.';
    return null;
  }
  if (signal?.aborted) return null;
  // Teardown structures exist before the contribution is invoked, so streams
  // opened by mount are tracked even when mount throws or rejects.
  const sockets = new Set();
  // Owner lifetime: retiring, disposing, or invalidating the owner aborts
  // in-flight requests. Update/suspend/resume never touch it.
  const lifetime = new AbortController();
  let invalidated = false;
  let invalidationReason = 'authority-revoked';
  let state = 'active';
  let owner = null;
  const isTerminal = () => invalidated || state === 'retired' || state === 'disposed';
  const closeSockets = () => {
    for (const socket of sockets) {
      try {
        socket.close();
      } catch {
        // A closing socket never blocks teardown.
      }
    }
    sockets.clear();
  };
  const invalidate = (reason) => {
    if (invalidated) return;
    invalidated = true;
    invalidationReason = reason;
    signal?.removeEventListener('abort', onHostAbort);
    lifetime.abort();
    closeSockets();
    owner?.retire(reason);
  };
  function onHostAbort() {
    try {
      invalidate('authority-revoked');
    } catch (error) {
      console.error('Could not retire aborted contribution', error);
    }
  }
  signal?.addEventListener('abort', onHostAbort, {once: true});
  const rawRequest = createScopedRequest(parsed.apiHref, parsed.sessionGeneration, fetcher);
  const request = (relativePath, options = {}) => {
    if (isTerminal()) throw new TypeError('Contribution transport is retired');
    if (options.signal?.aborted) return Promise.reject(new DOMException('This operation was aborted', 'AbortError'));
    let signal = lifetime.signal;
    if (options.signal) {
      // Native fan-in for the actual supported runtime: Soda's existing
      // transport (soda-extension.ts) already composes with
      // AbortSignal.any and no compatibility branch, and the pinned
      // HappyDOM/Chrome runtime provides it. The older Chrome 109
      // browserslist note does not describe a consumer that needs a
      // manual listener framework; building one retained a listener
      // pair on the persistent owner for every completed request.
      // This module adds no manual forwarding listeners to retain,
      // and the native signal still aborts in-flight body reads on
      // caller or owner abort.
      signal = AbortSignal.any([options.signal, lifetime.signal]);
    }
    const pending = rawRequest(relativePath, {...options, signal});
    return Promise.resolve(pending).then((response) => {
      if (isTerminal()) throw new DOMException('Contribution transport is retired', 'AbortError');
      return response;
    });
  };
  const stream = (...args) => {
    if (isTerminal()) throw new TypeError('Contribution transport is retired');
    const contribution = createScopedStream(parsed.apiHref, websocketClass)(...args);
    const {socket} = contribution;
    if (socket.readyState !== 3) {
      sockets.add(socket);
      socket.addEventListener?.('close', () => sockets.delete(socket), {once: true});
    }
    return contribution;
  };
  const navigate = (path, {replace = false} = {}) => {
    if (isTerminal()) throw new TypeError('Contribution transport is retired');
    if (typeof path !== 'string' || path === '') throw new TypeError('Navigation path is invalid');
    if (typeof replace !== 'boolean') throw new TypeError('Navigation history option is invalid');
    navigator(path, {replace});
  };
  const context = {
    extensionId: parsed.extensionId,
    pageId: parsed.mode === 'page' ? parsed.contributionId : undefined,
    panelId: parsed.mode === 'panel' ? parsed.contributionId : undefined,
    ownerId: parsed.ownerId,
    apiBase: parsed.apiHref,
    assetBase: parsed.assetHref,
    appSubUrl: parsed.appSubUrl,
    sessionGeneration: parsed.sessionGeneration,
    contractVersion: browserContractVersion,
    repositoryId: parsed.repositoryId,
    request,
    stream,
    navigate,
  };
  let module;
  try {
    module = await importer(parsed.entryHref);
  } catch (error) {
    if (invalidated) return null;
    invalidate('contribution-unavailable');
    console.error('Could not load workspace view', error);
    root.textContent = 'Could not load this workspace view.';
    return null;
  }
  if (invalidated) return null;
  if (typeof module.mount !== 'function') {
    invalidate('contribution-unavailable');
    root.textContent = 'Could not load this workspace view.';
    return null;
  }
  let handle = {};
  try {
    handle = normalizeHandle(await module.mount(root, context));
  } catch (error) {
    invalidate('contribution-unavailable');
    console.error('Could not load workspace view', error);
    root.textContent = 'Could not load this workspace view.';
    return null;
  }
  if (invalidated) {
    // The mount settled after host invalidation: never expose it. Mirror
    // settled teardown so retire-only handles release their resources.
    try {
      handle.retire?.(invalidationReason);
    } finally {
      handle.dispose?.();
    }
    return null;
  }
  const disposeHandle = () => {
    // Retire already disposes the handle; both states are terminal.
    if (state === 'disposed' || state === 'retired') return;
    state = 'disposed';
    signal?.removeEventListener('abort', onHostAbort);
    lifetime.abort();
    closeSockets();
    handle.dispose?.();
  };
  owner = {
    root,
    context,
    update: (presentation) => {
      if (state !== 'active' && state !== 'suspended') return;
      handle.update?.(presentation);
    },
    suspend: () => {
      // Shelter or session revalidation conceals the owner without dropping
      // its sockets. Only retire() closes them.
      if (state !== 'active') return;
      state = 'suspended';
      handle.suspend?.();
    },
    resume: (validation) => {
      if (state === 'retired' || state === 'disposed') return;
      // Verified resumption: only the admitted session generation may
      // resume retained private state. A mismatch (including a missing
      // validation) is an account/session change, never a resumption:
      // retire so private state and transport cannot continue stale.
      if (validation?.sessionGeneration !== parsed.sessionGeneration) {
        owner.retire('session-changed');
        return;
      }
      if (state !== 'suspended') return;
      state = 'active';
      handle.resume?.(validation);
    },
    retire: (reason) => {
      if (state === 'retired' || state === 'disposed') return;
      state = 'retired';
      signal?.removeEventListener('abort', onHostAbort);
      lifetime.abort();
      closeSockets();
      try {
        handle.retire?.(reason);
      } finally {
        handle.dispose?.();
      }
    },
    dispose: disposeHandle,
  };
  return owner;
}
