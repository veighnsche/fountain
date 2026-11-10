import {vi} from 'vitest';
import {readFileSync} from 'node:fs';
import {currentPageGeneration, initPageFeatures, navigateNative, registerPageFeature, retirePageFeatures} from '../native-lifecycle.js';
import {classifyWorkspaceNavigation, workspacePath, workspaceDirectPath, initExtensionPages, initExtensionWorkspace} from './extensions.js';
import {mount as mountExamplePanel} from '../../../contrib/extensions/example/assets/panel.js';

const origin = 'https://forge.example';
const base = `${origin}/team/repo`;

test('repository product tabs precede native unit tabs in the overflow menu', () => {
  const template = readFileSync('templates/repo/header.tmpl', 'utf8');
  const productTabs = template.indexOf('{{with .ExtensionPages}}{{range index . "repository"}}');
  const codeTab = template.indexOf('{{if .Permission.CanRead $.UnitTypeCode}}', template.indexOf('<overflow-menu'));
  expect(productTabs).toBeGreaterThan(template.indexOf('<overflow-menu'));
  expect(productTabs).toBeLessThan(codeTab);
  expect(template.match(/range index . "repository"/g)).toHaveLength(1);
  expect(template.slice(productTabs, codeTab)).toContain('{{if ne .Permission "admin"}}');
});

test('admits native same-origin paths and preserves deep links', () => {
  expect(classifyWorkspaceNavigation('/team/repo/issues/7?sort=oldest#comment-1', base, origin)).toEqual({
    kind: 'native',
    url: `${origin}/team/repo/issues/7?sort=oldest#comment-1`,
    path: '/team/repo/issues/7?sort=oldest#comment-1',
  });
  expect(workspacePath('/team/repo/issues/7', base, origin)).toBe('/team/repo/issues/7');
  expect(workspacePath('/workspace', base, origin)).toBe('/workspace');
  expect(workspacePath('/team/repo/workspace', base, origin)).toBe('/team/repo/workspace');
  expect(workspacePath('/workspace/new', base, origin)).toBe('/workspace/new');
  expect(workspacePath('/workspace/requests/d0123456789abcdef01234567', base, origin)).toBe('/workspace/requests/d0123456789abcdef01234567');
});

test('classifies the workspace root and declared subroutes as direct', () => {
  // Declared patterns as published by the host (NFX-C04/H-ROUTE).
  // Parameter values are bounded opaque segments: no product-ID shape
  // rule, so consumer-rejected values are still direct here. Their
  // repository form is direct too (NFX-C07): the host registers
  // "/{owner}/{repo}/workspace/*" generically and the transplant still
  // validates the entered document before joining it.
  const patterns = ['/workspace/new', '/workspace/requests/{request}'];
  for (const [pathname, sub] of [['/workspace', ''], ['/team/repo/workspace', ''], ['/forge/workspace', '/forge'],
    ['/workspace/new', ''], ['/forge/workspace/new', '/forge'],
    ['/workspace/requests/d0123456789abcdef01234567', ''], ['/forge/workspace/requests/d0123456789abcdef01234567', '/forge'],
    ['/workspace/requests/not-an-id', ''], ['/workspace/requests/ABC-123', ''],
    ['/user2/repo1/workspace/new', ''], ['/user2/repo1/workspace/requests/d0123456789abcdef01234567', ''],
    ['/forge/team/repo/workspace/new', '/forge'], ['/forge/team/repo/workspace/requests/abc', '/forge']]) {
    expect(workspaceDirectPath(pathname, sub, patterns)).toBe(true);
  }
  for (const [pathname, sub] of [['/workspace/requests', ''], ['/workspace/requests/', ''],
    ['/workspace/requests/d0123456789abcdef01234567/extra', ''],
    ['/workspace/new/extra', ''], ['/workspace/news', ''], ['/workspace/old', ''],
    ['/user2/repo1/workspace/old', ''], ['/user2/repo1/workspace/requests/abc/extra', ''],
    ['/workspace/new', '/forge'], ['/other/workspace/new', '/forge'], ['/forgefoo/workspace/new', '/forge'], ['/issues', '']]) {
    expect(workspaceDirectPath(pathname, sub, patterns)).toBe(false);
  }
  // Without declarations only workspace roots are direct.
  expect(workspaceDirectPath('/workspace/new', '')).toBe(false);
  expect(workspaceDirectPath('/workspace/requests/abc', '')).toBe(false);
  expect(workspaceDirectPath('/user2/repo1/workspace/new', '')).toBe(false);
});

test('keeps auth and external destinations for top-level navigation', () => {
  expect(classifyWorkspaceNavigation('/user/login?redirect_to=%2Fteam', base, origin).kind).toBe('top');
  expect(classifyWorkspaceNavigation('/user/logout', base, origin).kind).toBe('top');
  expect(classifyWorkspaceNavigation('/user/activate?code=abc', base, origin).kind).toBe('top');
  expect(classifyWorkspaceNavigation('/user/two_factor', base, origin).kind).toBe('top');
  expect(classifyWorkspaceNavigation('/user/webauthn', base, origin).kind).toBe('top');
  expect(classifyWorkspaceNavigation('/user/recover_account', base, origin).kind).toBe('top');
  expect(classifyWorkspaceNavigation('/user/settings/security/two_factor/enroll', base, origin).kind).toBe('top');
  expect(classifyWorkspaceNavigation('/user/settings/applications', base, origin).kind).toBe('top');
  expect(classifyWorkspaceNavigation('/oauth2/authorize', base, origin).kind).toBe('top');
  expect(classifyWorkspaceNavigation('/login/openid', base, origin).kind).toBe('top');
  expect(classifyWorkspaceNavigation('/-/native/session', base, origin).kind).toBe('reject');
  expect(classifyWorkspaceNavigation('/repo?access_token=secret', base, origin).kind).toBe('top');
  expect(classifyWorkspaceNavigation('/repo?monkey=usual', base, origin).kind).toBe('native');
  expect(classifyWorkspaceNavigation('https://elsewhere.example/', base, origin).kind).toBe('top');
  expect(classifyWorkspaceNavigation('data:text/html,unsafe', base, origin).kind).toBe('reject');
  expect(workspacePath('//elsewhere.example/', base, origin)).toBeNull();
  expect(workspacePath('/team%2frepo', base, origin)).toBeNull();
  expect(workspacePath('/team%5crepo', base, origin)).toBeNull();
  expect(workspacePath('/elsewhere', `${origin}/forge/team/repo`, origin, '/forge')).toBeNull();
  expect(workspacePath('/forge/team', `${origin}/forge/team/repo`, origin, '/forge')).toBe('/forge/team');
});

// Mount validation, scoped transport and the mount/update/suspend/resume/retire
// lifecycle are owned by extension-mount.js and covered there; these tests
// cover host mounting, session checks and navigation behavior.
test('keeps a mounted panel alive when its page enters BFCache', async () => {
  document.body.innerHTML = '<div data-extension-panel data-extension-contract-version="1" data-app-sub-url="" data-extension-id="example" data-extension-panel-id="notes" data-extension-entry="/-/native/assets/example/panel.js" data-extension-asset-base="/-/native/assets/example/" data-extension-api-base="/-/native/panels/example/notes/api/" data-extension-session-generation="session-a"></div>';
  const dispose = vi.fn();
  initExtensionPages(async () => ({mount: () => dispose}));
  await new Promise((resolve) => setTimeout(resolve, 0));
  const cached = new Event('pagehide');
  Object.defineProperty(cached, 'persisted', {value: true});
  window.dispatchEvent(cached);
  expect(dispose).not.toHaveBeenCalled();
  const final = new Event('pagehide');
  Object.defineProperty(final, 'persisted', {value: false});
  window.dispatchEvent(final);
  expect(dispose).toHaveBeenCalledOnce();
});

test('removes a cached extension page when the native session changes', async () => {
  document.body.innerHTML = '<div data-extension-page data-extension-contract-version="1" data-app-sub-url="" data-extension-id="example" data-extension-page-id="home" data-extension-entry="/-/native/assets/example/page.js" data-extension-asset-base="/-/native/assets/example/" data-extension-api-base="/-/native/pages/example/home/api/" data-extension-session-generation="session-a">private page</div>';
  const root = document.querySelector('[data-extension-page]');
  const dispose = vi.fn();
  const fetcher = vi.fn().mockResolvedValue({status: 204, headers: new Headers({'X-Extension-Session-Generation': 'session-b'})});
  initExtensionPages(async () => ({mount: () => dispose}), fetcher);
  await new Promise((resolve) => setTimeout(resolve, 0));
  const cached = new Event('pagehide');
  Object.defineProperty(cached, 'persisted', {value: true});
  window.dispatchEvent(cached);
  expect(root.style.display).toBe('none');
  const restored = new Event('pageshow');
  Object.defineProperty(restored, 'persisted', {value: true});
  window.dispatchEvent(restored);
  await vi.waitFor(() => expect(root.textContent).toContain('Your session changed'));
  expect(root.textContent).not.toContain('private page');
  expect(dispose).toHaveBeenCalledOnce();
  expect(fetcher).toHaveBeenCalledWith(`${window.location.origin}/-/native/session`, {
    credentials: 'same-origin', cache: 'no-store', redirect: 'manual',
  });
  window.dispatchEvent(new Event('pagehide'));
});

test('delayed revalidation conceals page and panel together, then restores or retires', async () => {
  document.body.innerHTML = liveMarkup({
    content: directContent(`Global Workspace${pageRootMarkup()}`),
    home: homeMarkup({panels: panelRootMarkup()}),
  });
  window.history.replaceState(null, '', '/workspace');
  const pageRoot = document.querySelector('[data-extension-page]');
  const panelRoot = document.querySelector('[data-extension-panel]');
  const home = document.querySelector('[data-extension-panels]');
  const pageSuspend = vi.fn();
  const pageResume = vi.fn();
  const pageDispose = vi.fn();
  const panelSuspend = vi.fn();
  const panelResume = vi.fn();
  const panelDispose = vi.fn();
  let mountedCount = 0;
  let bothMounted;
  const bothPromise = new Promise((resolve) => { bothMounted = resolve });
  const importer = vi.fn(async () => ({mount: (root) => {
    mountedCount++;
    if (mountedCount === 2) bothMounted();
    return root.hasAttribute('data-extension-page') ?
      {dispose: pageDispose, suspend: pageSuspend, resume: pageResume} :
      {dispose: panelDispose, suspend: panelSuspend, resume: panelResume};
  }}));
  let resolveCheck;
  const fetcher = vi.fn(() => new Promise((resolve) => { resolveCheck = resolve }));
  initExtensionPages(importer, fetcher);
  await bothPromise;
  await new Promise((resolve) => setTimeout(resolve, 0));
  const cached = () => {
    const event = new Event('pagehide');
    Object.defineProperty(event, 'persisted', {value: true});
    return event;
  };
  const restored = () => {
    const event = new Event('pageshow');
    Object.defineProperty(event, 'persisted', {value: true});
    return event;
  };
  window.dispatchEvent(cached());
  expect(pageRoot.style.display).toBe('none');
  expect(home.style.display).toBe('none');
  expect(pageSuspend).toHaveBeenCalledOnce();
  expect(panelSuspend).toHaveBeenCalledOnce();
  window.dispatchEvent(restored());
  await vi.waitFor(() => expect(fetcher).toHaveBeenCalledTimes(1));
  expect(pageRoot.style.display).toBe('none');
  expect(home.style.display).toBe('none');
  resolveCheck({status: 204, headers: new Headers({'X-Extension-Session-Generation': 'session-a'})});
  await vi.waitFor(() => expect(pageRoot.style.display).toBe(''));
  expect(home.style.display).toBe('');
  expect(pageResume).toHaveBeenCalledOnce();
  expect(panelResume).toHaveBeenCalledOnce();
  expect(pageRoot.isConnected).toBe(true);
  expect(panelRoot.isConnected).toBe(true);
  expect(pageDispose).not.toHaveBeenCalled();
  expect(panelDispose).not.toHaveBeenCalled();
  window.dispatchEvent(cached());
  window.dispatchEvent(restored());
  await vi.waitFor(() => expect(fetcher).toHaveBeenCalledTimes(2));
  resolveCheck({status: 204, headers: new Headers({'X-Extension-Session-Generation': 'session-b'})});
  await vi.waitFor(() => expect(pageRoot.textContent).toContain('Your session changed'));
  expect(home.textContent).toContain('Your session changed');
  expect(pageDispose).toHaveBeenCalledOnce();
  expect(panelDispose).toHaveBeenCalledOnce();
  window.dispatchEvent(new Event('pagehide'));
});

test('hidden and focus revalidation suspend first and resume only after verification', async () => {
  document.body.innerHTML = liveMarkup({
    content: directContent(`Global Workspace${pageRootMarkup()}`),
    home: homeMarkup({panels: panelRootMarkup()}),
  });
  window.history.replaceState(null, '', '/workspace');
  const pageRoot = document.querySelector('[data-extension-page]');
  const home = document.querySelector('[data-extension-panels]');
  const suspends = [];
  const resumes = [];
  const disposes = [];
  let mountedCount = 0;
  let bothMounted;
  const bothPromise = new Promise((resolve) => { bothMounted = resolve });
  const importer = vi.fn(async () => ({mount: () => {
    mountedCount++;
    if (mountedCount === 2) bothMounted();
    const suspend = vi.fn();
    const resume = vi.fn();
    const dispose = vi.fn();
    suspends.push(suspend);
    resumes.push(resume);
    disposes.push(dispose);
    return {dispose, suspend, resume};
  }}));
  let resolveCheck;
  const fetcher = vi.fn(() => new Promise((resolve) => { resolveCheck = resolve }));
  initExtensionPages(importer, fetcher);
  await bothPromise;
  await new Promise((resolve) => setTimeout(resolve, 0));
  // Document hidden: host suspension without any session fetch.
  Object.defineProperty(document, 'visibilityState', {configurable: true, value: 'hidden'});
  document.dispatchEvent(new Event('visibilitychange'));
  for (const suspend of suspends) expect(suspend).toHaveBeenCalledOnce();
  expect(fetcher).not.toHaveBeenCalled();
  Object.defineProperty(document, 'visibilityState', {configurable: true, value: 'visible'});
  // Focus revalidation stays suspended and concealed until verified.
  window.dispatchEvent(new Event('focus'));
  await vi.waitFor(() => expect(fetcher).toHaveBeenCalledTimes(1));
  expect(pageRoot.style.display).toBe('none');
  expect(home.style.display).toBe('none');
  for (const resume of resumes) expect(resume).not.toHaveBeenCalled();
  resolveCheck({status: 204, headers: new Headers({'X-Extension-Session-Generation': 'session-a'})});
  await vi.waitFor(() => expect(pageRoot.style.display).toBe(''));
  for (const resume of resumes) expect(resume).toHaveBeenCalledOnce();
  for (const dispose of disposes) expect(dispose).not.toHaveBeenCalled();
  // Account change on the next revalidation retires instead of resuming.
  window.dispatchEvent(new Event('focus'));
  await vi.waitFor(() => expect(fetcher).toHaveBeenCalledTimes(2));
  for (const suspend of suspends) expect(suspend).toHaveBeenCalledTimes(2);
  resolveCheck({status: 204, headers: new Headers({'X-Extension-Session-Generation': 'session-b'})});
  await vi.waitFor(() => expect(pageRoot.textContent).toContain('Your session changed'));
  for (const resume of resumes) expect(resume).toHaveBeenCalledOnce();
  for (const dispose of disposes) expect(dispose).toHaveBeenCalledOnce();
  delete document.visibilityState;
  window.dispatchEvent(new Event('pagehide'));
});

test('the real notes panel retains private state and stream only through verified resumption', async () => {
  const sockets = [];
  class HostSocket extends EventTarget {
    static OPEN = 1;
    static CLOSED = 3;
    readyState = 1;
    send = vi.fn();
    constructor() {
      super();
      sockets.push(this);
    }
    close() {
      this.readyState = 3;
      this.dispatchEvent(new Event('close'));
    }
  }
  const originalWebSocket = globalThis.WebSocket;
  globalThis.WebSocket = HostSocket;
  document.body.innerHTML = liveMarkup({
    content: directContent('Global Workspace'),
    home: homeMarkup({panels: panelRootMarkup()}),
  });
  window.history.replaceState(null, '', '/workspace');
  let resolveCheck;
  const fetcher = vi.fn((url) => {
    if (String(url).includes('/-/native/session')) return new Promise((resolve) => { resolveCheck = resolve });
    return Promise.resolve({ok: true, status: 200, text: async () => 'private notes'});
  });
  const cached = persistedPageHideEvent;
  const restored = persistedPageShowEvent;
  try {
    initExtensionPages(async () => ({mount: mountExamplePanel}), fetcher);
    const input = await vi.waitFor(() => {
      const field = document.querySelector('textarea');
      expect(field?.value).toBe('private notes');
      return field;
    });
    expect(sockets).toHaveLength(1);
    window.dispatchEvent(cached());
    expect(document.querySelector('[data-extension-panel]').hidden).toBe(true);
    expect(sockets[0].readyState).toBe(1);
    window.dispatchEvent(restored());
    await vi.waitFor(() => expect(fetcher.mock.calls.some(([url]) => String(url).includes('/-/native/session'))).toBe(true));
    expect(document.querySelector('[data-extension-panel]').hidden).toBe(true);
    resolveCheck({status: 204, headers: new Headers({'X-Extension-Session-Generation': 'session-a'})});
    await vi.waitFor(() => expect(document.querySelector('[data-extension-panel]').hidden).toBe(false));
    expect(input.value).toBe('private notes');
    expect(sockets[0].readyState).toBe(1);
    window.dispatchEvent(cached());
    window.dispatchEvent(restored());
    await vi.waitFor(() => expect(fetcher.mock.calls.filter(([url]) => String(url).includes('/-/native/session'))).toHaveLength(2));
    resolveCheck({status: 204, headers: new Headers({'X-Extension-Session-Generation': 'session-b'})});
    await vi.waitFor(() => expect(sockets[0].readyState).toBe(3));
    expect(input.value).toBe('');
    expect(document.querySelector('[data-extension-panels]').textContent).toContain('Your session changed');
  } finally {
    globalThis.WebSocket = originalWebSocket;
    window.dispatchEvent(new Event('pagehide'));
  }
});

// NFX-A02 region_adopt: fixtures mirror the real wire format. Every eligible
// document is a whole native page (head title, body data-page-data config,
// navbar, .page-content region, footer) with the persistent panels home from
// the shared layout (templates/base/footer.tmpl + extensions/panels.tmpl).
// Static fixture roots carry data-extension-contract-version="1" mirroring
// the server-rendered extension.BrowserContractVersion joint value (C03);
// version may be overridden or omitted for skew cases.
function pageRootMarkup({pageId = 'home', generation = 'session-a', appSubURL = '', extra = '', version = '1'} = {}) {
  const versionAttr = version === null ? '' : ` data-extension-contract-version="${version}"`;
  return `<div data-extension-page${versionAttr} data-app-sub-url="${appSubURL}" data-extension-id="example" data-extension-page-id="${pageId}" data-extension-entry="/-/native/assets/example/page.js" data-extension-asset-base="/-/native/assets/example/" data-extension-api-base="/-/native/pages/example/${pageId}/api/" data-extension-session-generation="${generation}"${extra}></div>`;
}

function persistedPageHideEvent() {
  const event = new Event('pagehide');
  Object.defineProperty(event, 'persisted', {value: true});
  return event;
}

function persistedPageShowEvent() {
  const event = new Event('pageshow');
  Object.defineProperty(event, 'persisted', {value: true});
  return event;
}

function panelRootMarkup({panelId = 'notes', generation = 'session-a', appSubURL = '', ownerId = '', apiBase = `/-/native/panels/example/${panelId}/api/`, version = '1', repositoryId = ''} = {}) {
  return `<div data-extension-panel data-extension-contract-version="${version}" data-app-sub-url="${appSubURL}" data-extension-id="example" data-extension-panel-id="${panelId}"${ownerId ? ` data-extension-owner-id="${ownerId}"` : ''} data-extension-entry="/-/native/assets/example/panel.js" data-extension-asset-base="/-/native/assets/example/" data-extension-api-base="${apiBase}" data-extension-session-generation="${generation}"${repositoryId ? ` data-extension-repository-id="${repositoryId}"` : ''}></div>`;
}

function homeMarkup({generation = 'session-a', appSubURL = '', hidden = false, panels = '', subroutes = '', repositoryId = ''} = {}) {
  return `<aside id="extension-panels" data-extension-panels data-app-sub-url="${appSubURL}" data-extension-session-generation="${generation}"${subroutes ? ` data-extension-subroutes='${subroutes}'` : ''}${repositoryId ? ` data-extension-repository-id="${repositoryId}"` : ''}${hidden ? ' hidden inert' : ''}>${panels}</aside>`;
}

const declaredSubroutes = '["/workspace/new","/workspace/requests/{request}"]';

function nativeDocument({title, pageData, navbar = '', content, home}) {
  return `<html><head><title>${title}</title></head><body data-page-data='${JSON.stringify(pageData)}'><nav id="navbar">${navbar}</nav><div class="full height">${content}</div>${home}<footer></footer></body></html>`;
}

function directContent(slot, cls = 'page-content extensions') {
  return `<div role="main" aria-label="Workspace" class="${cls}"><div class="ui container">${slot}</div></div>`;
}

function plainAdminContent(slot) {
  return `<div role="main" class="page-content admin"><div class="ui container flex-container"><div class="admin-navbar">Admin</div><div class="flex-container-main">${slot}</div></div></div>`;
}

function liveMarkup({content, home, navbar = ''}) {
  return `<nav id="navbar">${navbar}</nav><div class="full height">${content}</div>${home}<footer></footer>`;
}

function clickWorkspaceLink(href) {
  const link = document.createElement('a');
  link.href = href;
  document.body.append(link);
  link.click();
  link.remove();
}

function navigationResponse(html, generation = 'session-a') {
  return new Response(html, {headers: {'X-Extension-Session-Generation': generation}});
}

test('fragment-only navigation preserves native drafts and emits native hash behavior', async () => {
  const savedConfig = window.config;
  window.config = {pageData: {}};
  window.history.replaceState(null, '', '/user2/repo1?tab=code');
  document.body.innerHTML = liveMarkup({content: directContent('<input id="draft" value="pending"><a href="#note">Note</a><h2 id="user-content-note">Note</h2>'), home: homeMarkup({hidden: true})});
  const input = document.getElementById('draft');
  const fetcher = vi.fn();
  const hash = vi.fn();
  window.addEventListener('hashchange', hash);
  try {
    initExtensionWorkspace(fetcher);
    document.querySelector('a[href="#note"]').click();
    expect(window.location.pathname + window.location.search + window.location.hash).toBe('/user2/repo1?tab=code#note');
    expect(document.getElementById('draft')).toBe(input);
    expect(input.value).toBe('pending');
    expect(fetcher).not.toHaveBeenCalled();
    expect(hash).toHaveBeenCalledOnce();
  } finally {
    window.removeEventListener('hashchange', hash);
    window.dispatchEvent(new Event('pagehide'));
    window.config = savedConfig;
  }
});

test('transplant keeps one home, panels and address across contribution -> native', async () => {
  const savedConfig = window.config;
  window.history.replaceState({nativeState: 'retained'}, '', '/workspace');
  document.body.innerHTML = liveMarkup({
    content: directContent(`Global Workspace${pageRootMarkup()}`),
    home: homeMarkup({panels: '<div id="notes-owner">notes</div>'}),
    navbar: '<a class="item" href="/admin/users">Users</a><a class="item active" aria-current="page" href="/workspace">Workspace</a>',
  });
  window.config = {appSubUrl: '', pageData: {extension: {id: 'example'}}};
  const owner = document.getElementById('notes-owner');
  const home = document.querySelector('[data-extension-panels]');
  const adminPageData = {adminUserListSearchForm: {SortType: 'name'}};
  const workspacePageData = {extension: {id: 'example'}};
  const fetcher = vi.fn(async (url) => navigationResponse(url.includes('/admin/users') ?
    nativeDocument({title: 'Users - Forgejo', pageData: adminPageData, content: plainAdminContent('<form id="user-list-search-form"></form>'),
      navbar: '<a class="item" href="/admin/users">Users</a><a class="item" href="/workspace">Workspace</a>',
      home: homeMarkup({hidden: true})}) :
    nativeDocument({title: 'Workspace', pageData: workspacePageData, content: directContent(`Global Workspace${pageRootMarkup()}`),
      navbar: '<a class="item" href="/admin/users">Users</a><a class="item active" aria-current="page" href="/workspace">Workspace</a>',
      home: homeMarkup()})));
  const pushState = vi.spyOn(window.history, 'pushState');
  try {
    initExtensionWorkspace(fetcher);
    expect(home.hasAttribute('hidden')).toBe(false);
    clickWorkspaceLink('/admin/users?sort=oldest#list');
    await vi.waitFor(() => expect(window.location.pathname + window.location.search + window.location.hash).toBe('/admin/users?sort=oldest#list'));
    expect(document.title).toBe('Users - Forgejo');
    expect(document.querySelector('#user-list-search-form')).not.toBeNull();
    expect(window.config.pageData).toEqual(adminPageData);
    expect(document.querySelector('[data-extension-panels]')).toBe(home);
    expect(home.hasAttribute('hidden')).toBe(true);
    expect(home.hasAttribute('inert')).toBe(true);
    expect(document.getElementById('notes-owner')).toBe(owner);
    expect(owner.isConnected).toBe(true);
    expect(pushState).toHaveBeenCalledOnce();
    expect(document.querySelector('#navbar a[href="/workspace"]').classList.contains('active')).toBe(false);
    expect(document.querySelector('#navbar a[href="/workspace"]').hasAttribute('aria-current')).toBe(false);
    window.history.back();
    await vi.waitFor(() => expect(document.querySelector('.page-content').textContent).toContain('Global Workspace'));
    expect(home.hasAttribute('hidden')).toBe(false);
    expect(home.hasAttribute('inert')).toBe(false);
    expect(window.config.pageData).toEqual(workspacePageData);
    expect(window.history.state.nativeState).toBe('retained');
    expect(document.getElementById('notes-owner')).toBe(owner);
  } finally {
    pushState.mockRestore();
    window.dispatchEvent(new Event('pagehide'));
    window.config = savedConfig;
  }
});

test('a panel mounted on a hidden native-first home receives its initial non-visible presentation', async () => {
  window.history.replaceState(null, '', '/admin/users');
  document.body.innerHTML = liveMarkup({
    content: plainAdminContent('<form id="user-list-search-form"></form>'),
    home: homeMarkup({hidden: true, panels: panelRootMarkup()}),
  });
  const update = vi.fn();
  const dispose = vi.fn();
  let mountedResolve;
  const mountedPromise = new Promise((resolve) => { mountedResolve = resolve });
  const importer = vi.fn(async () => ({mount: () => {
    mountedResolve();
    return {dispose, update};
  }}));
  try {
    initExtensionPages(importer);
    await mountedPromise;
    await new Promise((resolve) => setTimeout(resolve, 0));
    expect(update).toHaveBeenCalledExactlyOnceWith({route: '/admin/users', params: {}, mode: 'panel', visible: false});
    expect(dispose).not.toHaveBeenCalled();
  } finally {
    window.dispatchEvent(new Event('pagehide'));
  }
});

test('transplant delivers ordered presentation updates to retained panels', async () => {
  const savedConfig = window.config;
  window.history.replaceState(null, '', '/workspace');
  document.body.innerHTML = liveMarkup({
    content: directContent('Global Workspace'),
    home: homeMarkup({panels: panelRootMarkup()}),
  });
  window.config = {appSubUrl: '', pageData: {extension: {id: 'example'}}};
  const update = vi.fn();
  const dispose = vi.fn();
  let mountedResolve;
  const mountedPromise = new Promise((resolve) => { mountedResolve = resolve });
  const importer = vi.fn(async () => ({mount: () => {
    mountedResolve();
    return {dispose, update};
  }}));
  const adminPageData = {adminUserListSearchForm: {SortType: 'name'}};
  const fetcher = vi.fn(async () => navigationResponse(nativeDocument({title: 'Users', pageData: adminPageData,
    content: plainAdminContent('<form></form>'), home: homeMarkup({hidden: true, panels: panelRootMarkup()})})));
  try {
    initExtensionPages(importer);
    await mountedPromise;
    await new Promise((resolve) => setTimeout(resolve, 0));
    expect(importer).toHaveBeenCalledOnce();
    initExtensionWorkspace(fetcher);
    clickWorkspaceLink('/admin/users?sort=name');
    await vi.waitFor(() => expect(update).toHaveBeenCalledOnce());
    expect(update).toHaveBeenCalledWith({route: '/admin/users?sort=name', params: {sort: 'name'}, mode: 'panel', visible: false});
    expect(dispose).not.toHaveBeenCalled();
    expect(importer).toHaveBeenCalledOnce();
  } finally {
    window.dispatchEvent(new Event('pagehide'));
    window.config = savedConfig;
  }
});

test('native admission withdrawal retires private panels and keeps the admitted Workspace owner and socket', async () => {
  const savedConfig = window.config;
  const savedWebSocket = globalThis.WebSocket;
  const sockets = [];
  globalThis.WebSocket = class {
    close = vi.fn();
    constructor() { sockets.push(this) }
  };
  const workspace = panelRootMarkup({panelId: 'workspace', ownerId: 'workspace', apiBase: '/-/native/pages/example/spaces/api/'});
  document.body.innerHTML = liveMarkup({content: directContent('Workspace'), home: homeMarkup({panels: `${workspace}<section class="extension-workspace-panel">Private panel${panelRootMarkup()}</section>`})});
  window.history.replaceState(null, '', '/workspace');
  window.config = {appSubUrl: '', pageData: {}};
  const home = document.querySelector('[data-extension-panels]');
  const workspaceRoot = home.querySelector('[data-extension-panel-id="workspace"]');
  const privateRoot = home.querySelector('[data-extension-panel-id="notes"]');
  const handles = new Map();
  const importer = vi.fn(async () => ({mount(root, context) {
    context.stream('events');
    root.textContent = context.panelId === 'notes' ? 'Private retained state' : 'Workspace terminal';
    const handle = {update: vi.fn(), retire: vi.fn(), dispose: vi.fn()};
    handles.set(context.panelId, handle);
    return handle;
  }}));
  const fetcher = vi.fn(async () => navigationResponse(nativeDocument({title: 'Repository', pageData: {}, content: directContent('Native repository'),
    home: homeMarkup({hidden: true, repositoryId: '1', panels: panelRootMarkup({panelId: 'workspace', ownerId: 'workspace', apiBase: '/-/native/pages/example/spaces/api/', repositoryId: '1'})})})));
  try {
    initExtensionPages(importer);
    await vi.waitFor(() => expect(handles.size).toBe(2));
    initExtensionWorkspace(fetcher);
    clickWorkspaceLink('/user2/repo1');
    await vi.waitFor(() => expect(window.location.pathname).toBe('/user2/repo1'));
    expect(document.querySelector('[data-extension-panels]')).toBe(home);
    expect(home.querySelector('[data-extension-panel-id="workspace"]')).toBe(workspaceRoot);
    expect(workspaceRoot.textContent).toBe('Workspace terminal');
    expect(workspaceRoot.dataset.extensionRepositoryId).toBe('1');
    expect(handles.get('workspace').update).toHaveBeenCalledWith({route: '/user2/repo1', params: {}, repositoryId: '1', mode: 'panel', visible: false});
    expect(handles.get('workspace').retire).not.toHaveBeenCalled();
    expect(sockets[0].close).not.toHaveBeenCalled();
    expect(privateRoot.isConnected).toBe(false);
    expect(home.textContent).not.toContain('Private retained state');
    expect(home.querySelector('.extension-workspace-panel')).toBeNull();
    expect(handles.get('notes').retire).toHaveBeenCalledWith('authority-revoked');
    expect(handles.get('notes').dispose).toHaveBeenCalledOnce();
    expect(sockets[1].close).toHaveBeenCalledOnce();
    expect(importer).toHaveBeenCalledTimes(2);
  } finally {
    window.dispatchEvent(new Event('pagehide'));
    window.config = savedConfig;
    globalThis.WebSocket = savedWebSocket;
  }
});

test('a native home admits a new shared owner after empty boot without a competing page mount', async () => {
  const savedConfig = window.config;
  document.body.innerHTML = liveMarkup({content: directContent('Native page'), home: homeMarkup({hidden: true})});
  window.history.replaceState(null, '', '/admin/users');
  window.config = {appSubUrl: '', pageData: {}};
  const panel = panelRootMarkup({panelId: 'workspace', ownerId: 'workspace', apiBase: '/-/native/pages/example/spaces/api/'});
  const page = pageRootMarkup().replace('data-extension-entry=', 'data-extension-owner-id="workspace" data-extension-entry=');
  const importer = vi.fn(async () => ({mount: () => ({update: vi.fn(), dispose: vi.fn()})}));
  const fetcher = vi.fn(async () => navigationResponse(nativeDocument({title: 'Workspace', pageData: {}, content: directContent(page),
    home: homeMarkup({panels: `<section class="extension-workspace-panel"><h2>Workspace</h2>${panel}</section>`})})));
  try {
    initExtensionPages(importer);
    initExtensionWorkspace(fetcher);
    clickWorkspaceLink('/workspace');
    await vi.waitFor(() => expect(importer).toHaveBeenCalledOnce());
    const root = document.querySelector('[data-extension-panel]');
    expect(root.closest('.extension-workspace-panel').querySelector('h2').textContent).toBe('Workspace');
    expect(document.querySelector('[data-extension-page]').hasAttribute('data-extension-owner-presented')).toBe(true);
    expect(document.querySelector('[data-extension-page]').hasAttribute('data-extension-mounted')).toBe(false);
  } finally {
    window.dispatchEvent(new Event('pagehide'));
    window.config = savedConfig;
  }
});

test('a changed panel API retires its old admission before mounting the new native declaration', async () => {
  const savedConfig = window.config;
  document.body.innerHTML = liveMarkup({content: directContent('Workspace'), home: homeMarkup({panels: panelRootMarkup()})});
  window.history.replaceState(null, '', '/workspace');
  window.config = {appSubUrl: '', pageData: {}};
  const oldRoot = document.querySelector('[data-extension-panel]');
  const retire = vi.fn();
  const contexts = [];
  const importer = vi.fn(async () => ({mount(_root, context) { contexts.push(context); return {retire, dispose: vi.fn()} }}));
  const fetcher = vi.fn(async () => navigationResponse(nativeDocument({title: 'Repository', pageData: {}, content: directContent('Repository'),
    home: homeMarkup({hidden: true, panels: panelRootMarkup({apiBase: '/user2/repo1/-/native/panels/example/notes/api/'})})})));
  try {
    initExtensionPages(importer);
    await vi.waitFor(() => expect(contexts).toHaveLength(1));
    initExtensionWorkspace(fetcher);
    clickWorkspaceLink('/user2/repo1');
    await vi.waitFor(() => expect(contexts).toHaveLength(2));
    expect(retire).toHaveBeenCalledWith('authority-revoked');
    expect(oldRoot.isConnected).toBe(false);
    expect(document.querySelector('[data-extension-panel]')).not.toBe(oldRoot);
    expect(new URL(contexts[1].apiBase).pathname).toBe('/user2/repo1/-/native/panels/example/notes/api/');
  } finally {
    window.dispatchEvent(new Event('pagehide'));
    window.config = savedConfig;
  }
});

test('native withdrawal aborts a panel import before it can publish private state', async () => {
  const savedConfig = window.config;
  document.body.innerHTML = liveMarkup({content: directContent('Workspace'), home: homeMarkup({panels: panelRootMarkup()})});
  window.history.replaceState(null, '', '/workspace');
  window.config = {appSubUrl: '', pageData: {}};
  const oldRoot = document.querySelector('[data-extension-panel]');
  let finishImport;
  const mount = vi.fn();
  const importer = vi.fn(() => new Promise((resolve) => { finishImport = resolve }));
  const fetcher = vi.fn(async () => navigationResponse(nativeDocument({title: 'Users', pageData: {}, content: plainAdminContent('Users'), home: homeMarkup({hidden: true})})));
  try {
    initExtensionPages(importer);
    await vi.waitFor(() => expect(importer).toHaveBeenCalledOnce());
    initExtensionWorkspace(fetcher);
    clickWorkspaceLink('/admin/users');
    await vi.waitFor(() => expect(oldRoot.isConnected).toBe(false));
    finishImport({mount});
    await new Promise((resolve) => setTimeout(resolve, 0));
    expect(mount).not.toHaveBeenCalled();
    expect(document.querySelector('[data-extension-panel]')).toBeNull();
  } finally {
    window.dispatchEvent(new Event('pagehide'));
    window.config = savedConfig;
  }
});

test('withdrawn private panel leaves the document even if its consumer cleanup fails', async () => {
  const savedConfig = window.config;
  document.body.innerHTML = liveMarkup({content: directContent('Workspace'), home: homeMarkup({panels: panelRootMarkup()})});
  window.history.replaceState(null, '', '/workspace');
  window.config = {appSubUrl: '', pageData: {}};
  const oldRoot = document.querySelector('[data-extension-panel]');
  const error = new Error('consumer cleanup failed');
  const importer = vi.fn(async () => ({mount: () => ({retire: () => { throw error }, dispose: vi.fn()})}));
  const report = vi.spyOn(console, 'error').mockImplementation(() => {});
  const fetcher = vi.fn(async () => navigationResponse(nativeDocument({title: 'Users', pageData: {}, content: plainAdminContent('Users'), home: homeMarkup({hidden: true})})));
  try {
    initExtensionPages(importer);
    await vi.waitFor(() => expect(oldRoot.hasAttribute('data-extension-mounted')).toBe(true));
    await new Promise((resolve) => setTimeout(resolve, 0));
    initExtensionWorkspace(fetcher);
    clickWorkspaceLink('/admin/users');
    await vi.waitFor(() => expect(window.location.pathname).toBe('/admin/users'));
    expect(oldRoot.isConnected).toBe(false);
    expect(oldRoot.hidden).toBe(true);
    expect(report).toHaveBeenCalledWith('Could not retire withdrawn contribution', error);
  } finally {
    report.mockRestore();
    window.dispatchEvent(new Event('pagehide'));
    window.config = savedConfig;
  }
});

test('a declared shared owner mounts once on its retained panel root', async () => {
  const savedConfig = window.config;
  window.history.replaceState(null, '', '/workspace');
  const ownerAttrs = 'data-extension-owner-id="workspace" data-extension-entry="/-/native/assets/example/main.js" data-extension-api-base="/-/native/pages/example/spaces/api/"';
  document.body.innerHTML = liveMarkup({
    content: directContent(`Global Workspace<div data-extension-page data-extension-contract-version="1" data-app-sub-url="" data-extension-id="example" data-extension-page-id="spaces" ${ownerAttrs} data-extension-asset-base="/-/native/assets/example/" data-extension-session-generation="session-a"></div>`),
    home: homeMarkup({panels: `<div data-extension-panel data-extension-contract-version="1" data-app-sub-url="" data-extension-id="example" data-extension-panel-id="workspace" ${ownerAttrs} data-extension-asset-base="/-/native/assets/example/" data-extension-session-generation="session-a"></div>`}),
  });
  window.config = {appSubUrl: '', pageData: {extension: {id: 'example'}}};
  const mounts = [];
  const importer = vi.fn(async () => ({mount: (_root, context) => {
    mounts.push(context);
    return {dispose: vi.fn(), update: vi.fn()};
  }}));
  try {
    initExtensionPages(importer);
    await vi.waitFor(() => expect(mounts).toHaveLength(1));
    expect(mounts[0]).toEqual(expect.objectContaining({panelId: 'workspace', ownerId: 'workspace', pageId: undefined}));
    const pageRoot = document.querySelector('[data-extension-page]');
    expect(pageRoot.hasAttribute('data-extension-owner-presented')).toBe(true);
    expect(pageRoot.hasAttribute('data-extension-mounted')).toBe(false);
    expect(importer).toHaveBeenCalledOnce();
  } finally {
    window.dispatchEvent(new Event('pagehide'));
    window.config = savedConfig;
  }
});

test('owner-presented page slots emit no competing mount and the home supplies responsive space', () => {
  const content = readFileSync('templates/extensions/content.tmpl', 'utf8');
  expect(content).toContain('$ownerPresented');
  expect(content).toContain('{{if not $ownerPresented}}{{template "extensions/mount" .ExtensionMount}}');
  const mount = readFileSync('templates/extensions/mount.tmpl', 'utf8');
  expect(mount).toContain('class="extension-page-mount" data-extension-page');
  expect(content).toContain('extension-page-owner-presented');
  const css = readFileSync('web_src/css/features/extensions.css', 'utf8');
  expect(css).toContain('[data-extension-page][data-extension-owner-presented] { display: none !important; }');
  expect(css).toContain('.extension-workspace-panels:not([hidden])');
  expect(css).toContain('body:has(> #extension-panels:not([hidden])) > .full.height');
  expect(css).toContain('@media (max-width: 767.98px)');
});

test('a shared owner without a page root receives initial page-mode presentation', async () => {
  const savedConfig = window.config;
  window.history.replaceState(null, '', '/workspace');
  const ownerAttrs = 'data-extension-owner-id="workspace" data-extension-direct-patterns=\'["/workspace"]\' data-extension-entry="/-/native/assets/example/main.js" data-extension-api-base="/-/native/pages/example/spaces/api/"';
  // Server-suppressed page slot (NFX-A04/H-C): only the retained panel root.
  document.body.innerHTML = liveMarkup({
    content: directContent('Global Workspace'),
    home: homeMarkup({panels: `<div data-extension-panel data-extension-contract-version="1" data-app-sub-url="" data-extension-id="example" data-extension-panel-id="workspace" ${ownerAttrs} data-extension-asset-base="/-/native/assets/example/" data-extension-session-generation="session-a"></div>`}),
  });
  window.config = {appSubUrl: '', pageData: {extension: {id: 'example'}}};
  const update = vi.fn();
  const dispose = vi.fn();
  let mountedResolve;
  const mountedPromise = new Promise((resolve) => { mountedResolve = resolve });
  const importer = vi.fn(async () => ({mount: () => {
    mountedResolve();
    return {dispose, update};
  }}));
  try {
    initExtensionPages(importer);
    await mountedPromise;
    await new Promise((resolve) => setTimeout(resolve, 0));
    expect(importer).toHaveBeenCalledOnce();
    expect(document.querySelector('[data-extension-page]')).toBeNull();
    expect(update).toHaveBeenCalledExactlyOnceWith({route: '/workspace', params: {}, mode: 'page', visible: true});
    expect(dispose).not.toHaveBeenCalled();
  } finally {
    window.dispatchEvent(new Event('pagehide'));
    window.config = savedConfig;
  }
});

test('page owners persist a plain leg parked and readopt on return', async () => {
  const savedConfig = window.config;
  window.history.replaceState(null, '', '/workspace');
  document.body.innerHTML = liveMarkup({
    content: directContent(`Global Workspace${pageRootMarkup()}`),
    home: homeMarkup({panels: panelRootMarkup()}),
  });
  window.config = {appSubUrl: '', pageData: {extension: {id: 'example'}}};
  const pageRoot = document.querySelector('[data-extension-page]');
  const suspend = vi.fn();
  const resume = vi.fn();
  const update = vi.fn();
  const dispose = vi.fn();
  let mountedResolve;
  const mountedPromise = new Promise((resolve) => { mountedResolve = resolve });
  const importer = vi.fn(async () => ({mount: (root) => {
    if (root.hasAttribute('data-extension-page')) {
      mountedResolve();
      return {dispose, update, suspend, resume};
    }
    return {dispose: () => {}};
  }}));
  const fetcher = vi.fn(async (url) => navigationResponse(url.includes('/admin/users') ?
    nativeDocument({title: 'Users', pageData: {adminUserListSearchForm: {}}, content: plainAdminContent('<form></form>'), home: homeMarkup({hidden: true})}) :
    nativeDocument({title: 'Workspace', pageData: {extension: {id: 'example'}}, content: directContent(`Global Workspace${pageRootMarkup()}`), home: homeMarkup()})));
  try {
    initExtensionPages(importer);
    await mountedPromise;
    await new Promise((resolve) => setTimeout(resolve, 0));
    initExtensionWorkspace(fetcher);
    clickWorkspaceLink('/admin/users');
    await vi.waitFor(() => expect(window.location.pathname).toBe('/admin/users'));
    expect(suspend).toHaveBeenCalledOnce();
    expect(dispose).not.toHaveBeenCalled();
    expect(update).toHaveBeenCalledWith({route: '/admin/users', params: {}, mode: 'page', visible: false});
    expect(importer).toHaveBeenCalledTimes(2);
    expect(pageRoot.isConnected).toBe(true);
    expect(document.querySelector('[data-extension-page-park]').contains(pageRoot)).toBe(true);
    expect(document.querySelector('.page-content').contains(pageRoot)).toBe(false);
    clickWorkspaceLink('/workspace');
    await vi.waitFor(() => expect(window.location.pathname).toBe('/workspace'));
    await vi.waitFor(() => expect(resume).toHaveBeenCalledOnce());
    expect(resume).toHaveBeenCalledWith({sessionGeneration: 'session-a'});
    expect(update).toHaveBeenCalledWith({route: '/workspace', params: {}, mode: 'page', visible: true});
    expect(dispose).not.toHaveBeenCalled();
    expect(importer).toHaveBeenCalledTimes(2);
    expect(document.querySelector('.page-content').contains(pageRoot)).toBe(true);
    expect(pageRoot.isConnected).toBe(true);
  } finally {
    window.dispatchEvent(new Event('pagehide'));
    window.config = savedConfig;
  }
});

test.each([['missing', null], ['mismatched', '2']])('a %s contract version retires the parked owner without adopting or mounting', async (_label, version) => {
  const savedConfig = window.config;
  window.history.replaceState(null, '', '/workspace');
  document.body.innerHTML = liveMarkup({
    content: directContent(`Global Workspace${pageRootMarkup()}`),
    home: homeMarkup(),
  });
  window.config = {appSubUrl: '', pageData: {extension: {id: 'example'}}};
  const pageRoot = document.querySelector('[data-extension-page]');
  const retire = vi.fn();
  const dispose = vi.fn();
  let mountedResolve;
  const mountedPromise = new Promise((resolve) => { mountedResolve = resolve });
  const importer = vi.fn(async () => ({mount: () => {
    mountedResolve();
    return {dispose, retire};
  }}));
  const fetcher = vi.fn(async () => navigationResponse(nativeDocument({title: 'Workspace', pageData: {extension: {id: 'example'}},
    content: directContent(`Global Workspace${pageRootMarkup({version})}`), home: homeMarkup()})));
  try {
    initExtensionPages(importer);
    await mountedPromise;
    await new Promise((resolve) => setTimeout(resolve, 0));
    initExtensionWorkspace(fetcher);
    clickWorkspaceLink('/workspace?view=all');
    await vi.waitFor(() => expect(window.location.pathname + window.location.search).toBe('/workspace?view=all'));
    await vi.waitFor(() => expect(retire).toHaveBeenCalledWith('contribution-unavailable'));
    expect(dispose).toHaveBeenCalledOnce();
    expect(importer).toHaveBeenCalledOnce();
    expect(document.querySelector('.page-content [data-extension-page]')).toBeNull();
    expect(pageRoot.isConnected).toBe(false);
    expect(document.querySelector('[data-extension-page-park] [data-extension-page]')).toBeNull();
  } finally {
    window.dispatchEvent(new Event('pagehide'));
    window.config = savedConfig;
  }
});

test('a changed contribution parks the live owner and mounts the entered root', async () => {
  const savedConfig = window.config;
  window.history.replaceState(null, '', '/workspace');
  document.body.innerHTML = liveMarkup({
    content: directContent(`Global Workspace${pageRootMarkup()}`),
    home: homeMarkup({subroutes: declaredSubroutes}),
  });
  window.config = {appSubUrl: '', pageData: {extension: {id: 'example'}}};
  const homeRoot = document.querySelector('[data-extension-page]');
  const firstDispose = vi.fn();
  const firstResume = vi.fn();
  const secondMount = vi.fn();
  secondMount.mockReturnValue({dispose: () => {}});
  let mountedResolve;
  const mountedPromise = new Promise((resolve) => { mountedResolve = resolve });
  const firstMount = () => {
    mountedResolve();
    return {dispose: firstDispose, resume: firstResume};
  };
  const importer = vi.fn(async () => ({mount: firstMount}));
  const fetcher = vi.fn(async (url) => navigationResponse(url.includes('/workspace/new') ?
    nativeDocument({title: 'New Project', pageData: {extension: {id: 'example'}},
      content: directContent(`New Project${pageRootMarkup({pageId: 'new'})}`), home: homeMarkup()}) :
    nativeDocument({title: 'Workspace', pageData: {extension: {id: 'example'}},
      content: directContent(`Global Workspace${pageRootMarkup()}`), home: homeMarkup()})));
  try {
    initExtensionPages(importer);
    await mountedPromise;
    await new Promise((resolve) => setTimeout(resolve, 0));
    expect(importer).toHaveBeenCalledOnce();
    importer.mockImplementationOnce(async () => ({mount: secondMount}));
    initExtensionWorkspace(fetcher);
    clickWorkspaceLink('/workspace/new');
    await vi.waitFor(() => expect(window.location.pathname).toBe('/workspace/new'));
    await vi.waitFor(() => expect(secondMount).toHaveBeenCalledOnce());
    expect(firstDispose).not.toHaveBeenCalled();
    expect(importer).toHaveBeenCalledTimes(2);
    expect(document.querySelector('[data-extension-page-park]').contains(homeRoot)).toBe(true);
    clickWorkspaceLink('/workspace');
    await vi.waitFor(() => expect(window.location.pathname).toBe('/workspace'));
    await vi.waitFor(() => expect(firstResume).toHaveBeenCalledOnce());
    expect(firstDispose).not.toHaveBeenCalled();
    expect(importer).toHaveBeenCalledTimes(2);
    expect(document.querySelector('.page-content').contains(homeRoot)).toBe(true);
  } finally {
    window.dispatchEvent(new Event('pagehide'));
    window.config = savedConfig;
  }
});

test('the page shelter disposes the oldest parked owner past four', async () => {
  const savedConfig = window.config;
  window.history.replaceState(null, '', '/workspace');
  document.body.innerHTML = liveMarkup({
    content: directContent(`Global Workspace${pageRootMarkup()}`),
    home: homeMarkup({subroutes: declaredSubroutes}),
  });
  window.config = {appSubUrl: '', pageData: {extension: {id: 'example'}}};
  const homeRoot = document.querySelector('[data-extension-page]');
  const legs = [
    ['/workspace/new', 'new'],
    ['/workspace/requests/d000000000000000000000001', 'req1'],
    ['/workspace/requests/d000000000000000000000002', 'req2'],
    ['/team/repo/workspace', 'repohome'],
  ];
  const firstDispose = vi.fn();
  let mountedResolve;
  const mountedPromise = new Promise((resolve) => { mountedResolve = resolve });
  const importer = vi.fn(async () => ({mount: () => {
    mountedResolve();
    return {dispose: firstDispose};
  }}));
  const fetcher = vi.fn(async (url) => {
    const path = new URL(url, origin).pathname;
    const leg = legs.find(([prefix]) => path === prefix || path.startsWith(`${prefix}?`));
    const slot = leg ? `Leg ${leg[1]}${pageRootMarkup({pageId: leg[1]})}` : 'Users';
    const content = leg ? directContent(slot) : plainAdminContent('<form></form>');
    return navigationResponse(nativeDocument({title: 'Leg', pageData: {extension: {id: 'example'}},
      content, home: homeMarkup({hidden: !leg})}));
  });
  try {
    initExtensionPages(importer);
    await mountedPromise;
    await new Promise((resolve) => setTimeout(resolve, 0));
    importer.mockImplementation(async () => ({mount: () => ({dispose: () => {}})}));
    initExtensionWorkspace(fetcher);
    for (const [path] of legs) {
      clickWorkspaceLink(path);
      await vi.waitFor(() => expect(window.location.pathname).toBe(path));
    }
    clickWorkspaceLink('/admin/users');
    await vi.waitFor(() => expect(window.location.pathname).toBe('/admin/users'));
    await vi.waitFor(() => expect(firstDispose).toHaveBeenCalledOnce());
    expect(homeRoot.isConnected).toBe(false);
    expect(document.querySelector('[data-extension-page-park]').children.length).toBe(4);
    expect(importer).toHaveBeenCalledTimes(5);
  } finally {
    window.dispatchEvent(new Event('pagehide'));
    window.config = savedConfig;
  }
});

test('the shelter never evicts a persistent shared owner', async () => {
  const savedConfig = window.config;
  window.history.replaceState(null, '', '/workspace');
  document.body.innerHTML = liveMarkup({
    content: directContent(`Global Workspace${pageRootMarkup({extra: ' data-extension-persist'})}`),
    home: homeMarkup({subroutes: declaredSubroutes}),
  });
  window.config = {appSubUrl: '', pageData: {extension: {id: 'example'}}};
  const homeRoot = document.querySelector('[data-extension-page]');
  const legs = [
    ['/workspace/new', 'new'],
    ['/workspace/requests/d000000000000000000000001', 'req1'],
    ['/workspace/requests/d000000000000000000000002', 'req2'],
    ['/team/repo/workspace', 'repohome'],
  ];
  const disposes = [];
  let mountedResolve;
  const mountedPromise = new Promise((resolve) => { mountedResolve = resolve });
  const importer = vi.fn(async () => ({mount: () => {
    mountedResolve();
    const dispose = vi.fn();
    disposes.push(dispose);
    return {dispose};
  }}));
  const fetcher = vi.fn(async (url) => {
    const path = new URL(url, origin).pathname;
    const leg = legs.find(([prefix]) => path === prefix || path.startsWith(`${prefix}?`));
    const slot = leg ? `Leg ${leg[1]}${pageRootMarkup({pageId: leg[1]})}` : 'Users';
    const content = leg ? directContent(slot) : plainAdminContent('<form></form>');
    return navigationResponse(nativeDocument({title: 'Leg', pageData: {extension: {id: 'example'}},
      content, home: homeMarkup({hidden: !leg})}));
  });
  try {
    initExtensionPages(importer);
    await mountedPromise;
    await new Promise((resolve) => setTimeout(resolve, 0));
    initExtensionWorkspace(fetcher);
    for (const [path] of legs) {
      clickWorkspaceLink(path);
      await vi.waitFor(() => expect(window.location.pathname).toBe(path));
    }
    clickWorkspaceLink('/admin/users');
    await vi.waitFor(() => expect(window.location.pathname).toBe('/admin/users'));
    // Five parked roots, four slots: the persistent home root survives and
    // the oldest non-persistent root disposes instead.
    expect(disposes).toHaveLength(5);
    expect(disposes[0]).not.toHaveBeenCalled();
    expect(disposes[1]).toHaveBeenCalledOnce();
    expect(homeRoot.isConnected).toBe(true);
    expect(document.querySelector('[data-extension-page-park]').contains(homeRoot)).toBe(true);
    expect(document.querySelector('[data-extension-page-park]').children.length).toBe(4);
  } finally {
    window.dispatchEvent(new Event('pagehide'));
    window.config = savedConfig;
  }
});

test('leaves modified links, downloads and excluded destinations to native handling', async () => {
  const savedConfig = window.config;
  window.history.replaceState(null, '', '/workspace');
  document.body.innerHTML = liveMarkup({content: directContent('Global Workspace'), home: homeMarkup()});
  window.config = {appSubUrl: '', pageData: {extension: {id: 'example'}}};
  const fetcher = vi.fn(async () => navigationResponse(nativeDocument({title: 'Workspace', pageData: {},
    content: directContent(''), home: homeMarkup()})));
  try {
    initExtensionWorkspace(fetcher);
    const modified = document.createElement('a');
    modified.href = '/admin/users';
    document.body.append(modified);
    modified.dispatchEvent(new MouseEvent('click', {bubbles: true, cancelable: true, ctrlKey: true}));
    expect(fetcher).not.toHaveBeenCalled();
    modified.remove();
    const download = document.createElement('a');
    download.href = '/admin/users';
    download.download = 'users';
    document.body.append(download);
    download.dispatchEvent(new MouseEvent('click', {bubbles: true, cancelable: true}));
    expect(fetcher).not.toHaveBeenCalled();
    download.remove();
    const blocked = document.createElement('a');
    blocked.href = '/-/native/session';
    document.body.append(blocked);
    const rejected = new MouseEvent('click', {bubbles: true, cancelable: true});
    blocked.dispatchEvent(rejected);
    expect(rejected.defaultPrevented).toBe(true);
    expect(fetcher).not.toHaveBeenCalled();
    blocked.remove();
    await new Promise((resolve) => setTimeout(resolve, 0));
    expect(document.querySelector('.page-content').textContent).toBe('Global Workspace');
  } finally {
    window.dispatchEvent(new Event('pagehide'));
    window.config = savedConfig;
  }
});

test('transplants null pageData as empty config but full-loads a missing embed', async () => {
  const savedConfig = window.config;
  window.history.replaceState(null, '', '/workspace');
  document.body.innerHTML = liveMarkup({content: directContent('Global Workspace'), home: homeMarkup()});
  window.config = {appSubUrl: '', pageData: {extension: {id: 'example'}}};
  const fetcher = vi.fn(async (url) => {
    if (url.includes('null-config')) {
      return navigationResponse(`<html><head><title>Users</title></head><body data-page-data='null'><nav id="navbar"></nav>${plainAdminContent('<form></form>')}${homeMarkup({hidden: true})}<footer></footer></body></html>`);
    }
    return navigationResponse(`<html><head><title>Users</title></head><body><nav id="navbar"></nav>${plainAdminContent('<form></form>')}${homeMarkup({hidden: true})}<footer></footer></body></html>`);
  });
  const assign = vi.spyOn(window.location, 'assign').mockImplementation(() => {});
  try {
    initExtensionWorkspace(fetcher);
    clickWorkspaceLink('/admin/users?null-config=1');
    await vi.waitFor(() => expect(window.location.pathname + window.location.search).toBe('/admin/users?null-config=1'));
    expect(window.config.pageData).toEqual({});
    expect(assign).not.toHaveBeenCalled();
    clickWorkspaceLink('/admin/users?missing-embed=1');
    await vi.waitFor(() => expect(assign).toHaveBeenCalledWith(expect.stringContaining('/admin/users?missing-embed=1')));
  } finally {
    assign.mockRestore();
    window.dispatchEvent(new Event('pagehide'));
    window.config = savedConfig;
  }
});

test('admits ordinary rendered admin layouts through the native envelope', async () => {
  // Fixture mechanism check only: dashboard polling/morph still requires
  // the actual pinned HTMX real-browser proof.
  const savedConfig = window.config;
  window.history.replaceState(null, '', '/workspace');
  document.body.innerHTML = liveMarkup({
    content: directContent('Global Workspace'),
    home: homeMarkup({panels: '<div id="notes-owner">notes</div>'}),
    navbar: '<a href="/admin">Dashboard</a>',
  });
  window.config = {appSubUrl: '', pageData: {extension: {id: 'example'}}};
  const owner = document.getElementById('notes-owner');
  const fetcher = vi.fn(async () => navigationResponse(nativeDocument({
    title: 'Administration - Forgejo', pageData: {},
    content: plainAdminContent('<div hx-get="/admin/system_status" hx-trigger="every 5s" id="dashboard-status"></div>'),
    home: homeMarkup({hidden: true}),
  })));
  const assign = vi.spyOn(window.location, 'assign').mockImplementation(() => {});
  try {
    initExtensionWorkspace(fetcher);
    document.querySelector('#navbar a[href="/admin"]').click();
    await vi.waitFor(() => expect(window.location.pathname).toBe('/admin'));
    expect(document.getElementById('dashboard-status')).not.toBeNull();
    expect(window.config.pageData).toEqual({});
    expect(document.getElementById('notes-owner')).toBe(owner);
    expect(owner.isConnected).toBe(true);
    expect(assign).not.toHaveBeenCalled();
    clickWorkspaceLink('/admin/orgs');
    await vi.waitFor(() => expect(window.location.pathname).toBe('/admin/orgs'));
    expect(assign).not.toHaveBeenCalled();
    expect(fetcher).toHaveBeenCalledTimes(2);
  } finally {
    assign.mockRestore();
    window.dispatchEvent(new Event('pagehide'));
    window.config = savedConfig;
  }
});

test('retains the native document on an ordinary route with a valid render envelope', async () => {
  const savedConfig = window.config;
  window.history.replaceState(null, '', '/workspace');
  document.body.innerHTML = liveMarkup({content: directContent('Global Workspace'), home: homeMarkup()});
  window.config = {appSubUrl: '', pageData: {extension: {id: 'example'}}};
  const fetcher = vi.fn(async () => navigationResponse(nativeDocument({title: 'Workspace', pageData: {},
    content: directContent(''), home: homeMarkup()})));
  const assign = vi.spyOn(window.location, 'assign').mockImplementation(() => {});
  try {
    initExtensionWorkspace(fetcher);
    clickWorkspaceLink('/issues');
    await vi.waitFor(() => expect(window.location.pathname).toBe('/issues'));
    expect(fetcher).toHaveBeenCalledOnce();
    expect(assign).not.toHaveBeenCalled();
  } finally {
    assign.mockRestore();
    window.dispatchEvent(new Event('pagehide'));
    window.config = savedConfig;
  }
});

test('transplants repository documents and repository workspace subroutes in-shell', async () => {
  const savedConfig = window.config;
  window.history.replaceState(null, '', '/workspace');
  document.body.innerHTML = liveMarkup({
    content: directContent('Global Workspace'),
    home: homeMarkup({panels: '<div id="notes-owner">notes</div>', subroutes: declaredSubroutes}),
  });
  window.config = {appSubUrl: '', pageData: {extension: {id: 'example'}}};
  const owner = document.getElementById('notes-owner');
  const home = document.querySelector('[data-extension-panels]');
  const fetcher = vi.fn(async (url) => {
    if (url.includes('/team/repo/workspace/new')) {
      return navigationResponse(nativeDocument({title: 'New Project', pageData: {extension: {id: 'example'}},
        content: directContent('Repo New Project'), home: homeMarkup({subroutes: declaredSubroutes})}));
    }
    return navigationResponse(nativeDocument({title: 'team/repo - Forgejo', pageData: {},
      content: '<div role="main" class="page-content repository"><div class="ui container">Repo Home</div></div>',
      home: homeMarkup({hidden: true, subroutes: declaredSubroutes})}));
  });
  const assign = vi.spyOn(window.location, 'assign').mockImplementation(() => {});
  try {
    initExtensionWorkspace(fetcher);
    clickWorkspaceLink('/team/repo');
    await vi.waitFor(() => expect(window.location.pathname).toBe('/team/repo'));
    expect(document.title).toBe('team/repo - Forgejo');
    expect(document.querySelector('.page-content').textContent).toContain('Repo Home');
    expect(home.hasAttribute('hidden')).toBe(true);
    expect(document.getElementById('notes-owner')).toBe(owner);
    clickWorkspaceLink('/team/repo/workspace/new');
    await vi.waitFor(() => expect(window.location.pathname).toBe('/team/repo/workspace/new'));
    expect(document.querySelector('.page-content').textContent).toContain('Repo New Project');
    expect(home.hasAttribute('hidden')).toBe(false);
    expect(document.getElementById('notes-owner')).toBe(owner);
    expect(owner.isConnected).toBe(true);
    expect(assign).not.toHaveBeenCalled();
  } finally {
    assign.mockRestore();
    window.dispatchEvent(new Event('pagehide'));
    window.config = savedConfig;
  }
});

test('full-loads failed and partial transplant responses without joining them', async () => {
  const savedConfig = window.config;
  window.history.replaceState(null, '', '/workspace');
  document.body.innerHTML = liveMarkup({content: directContent('Global Workspace'), home: homeMarkup()});
  window.config = {appSubUrl: '', pageData: {extension: {id: 'example'}}};
  const assign = vi.spyOn(window.location, 'assign').mockImplementation(() => {});
  const fetcher = vi.fn(async (url) => {
    if (url.includes('broken=1')) return new Response('nope', {status: 500, headers: {'X-Extension-Session-Generation': 'session-a'}});
    if (url.includes('partial=1')) {
      return navigationResponse('<html><head><title>Partial</title></head><body><div>no content region</div></body></html>');
    }
    throw new Error('network down');
  });
  try {
    initExtensionWorkspace(fetcher);
    clickWorkspaceLink('/admin/users?broken=1');
    await vi.waitFor(() => expect(assign).toHaveBeenCalledWith(expect.stringContaining('/admin/users?broken=1')));
    expect(document.querySelector('.page-content').textContent).toBe('Global Workspace');
    clickWorkspaceLink('/admin/users?partial=1');
    await vi.waitFor(() => expect(assign).toHaveBeenCalledWith(expect.stringContaining('/admin/users?partial=1')));
    expect(document.querySelector('.page-content').textContent).toBe('Global Workspace');
    clickWorkspaceLink('/admin/users?down=1');
    await vi.waitFor(() => expect(assign).toHaveBeenCalledWith(expect.stringContaining('/admin/users?down=1')));
    expect(document.querySelector('.page-content').textContent).toBe('Global Workspace');
  } finally {
    assign.mockRestore();
    window.dispatchEvent(new Event('pagehide'));
    window.config = savedConfig;
  }
});

test('workspace uses its declared subpath for session checks while retaining the native deep link', async () => {
  document.body.innerHTML = liveMarkup({content: directContent('Global Workspace'), home: homeMarkup({appSubURL: '/forge'})});
  window.history.replaceState(null, '', '/forge/workspace#comment-1');
  const fetcher = vi.fn().mockResolvedValue({status: 204, headers: new Headers({'X-Extension-Session-Generation': 'session-a'})});
  initExtensionWorkspace(fetcher);
  window.dispatchEvent(new Event('focus'));
  await vi.waitFor(() => expect(fetcher).toHaveBeenCalledWith(`${window.location.origin}/forge/-/native/session`, {
    credentials: 'same-origin', cache: 'no-store', redirect: 'manual',
  }));
  expect(window.location.pathname + window.location.search + window.location.hash).toBe('/forge/workspace#comment-1');
  window.dispatchEvent(new Event('pagehide'));
});

test('rechecks a restored document and retires owners on account change', async () => {
  document.body.innerHTML = liveMarkup({
    content: directContent('Global Workspace'),
    home: homeMarkup({panels: panelRootMarkup()}),
  });
  window.history.replaceState(null, '', '/workspace');
  const home = document.querySelector('[data-extension-panels]');
  const dispose = vi.fn();
  const pagesFetcher = vi.fn().mockResolvedValue({status: 204, headers: new Headers({'X-Extension-Session-Generation': 'session-a'})});
  initExtensionPages(async () => ({mount: () => dispose}), pagesFetcher);
  await new Promise((resolve) => setTimeout(resolve, 0));
  const fetcher = vi.fn().mockResolvedValueOnce({status: 204, headers: new Headers({'X-Extension-Session-Generation': 'session-a'})})
    .mockResolvedValueOnce({status: 204, headers: new Headers({'X-Extension-Session-Generation': 'session-b'})});
  initExtensionWorkspace(fetcher);
  const cached = new Event('pagehide');
  Object.defineProperty(cached, 'persisted', {value: true});
  window.dispatchEvent(cached);
  const restored = new Event('pageshow');
  Object.defineProperty(restored, 'persisted', {value: true});
  window.dispatchEvent(restored);
  await vi.waitFor(() => expect(fetcher).toHaveBeenCalledTimes(1));
  await new Promise((resolve) => setTimeout(resolve, 0));
  expect(dispose).not.toHaveBeenCalled();
  expect(document.querySelector('.page-content').textContent).toBe('Global Workspace');
  expect(fetcher).toHaveBeenCalledWith(`${window.location.origin}/-/native/session`, {
    credentials: 'same-origin', cache: 'no-store', redirect: 'manual',
  });
  window.dispatchEvent(new Event('focus'));
  await vi.waitFor(() => expect(document.querySelector('.page-content').textContent).toContain('Your session changed'));
  expect(home.hasAttribute('hidden')).toBe(true);
  // The retired panel root stays as inert markup; its owner is gone.
  expect(home.querySelector('[data-extension-panel]')).not.toBeNull();
  expect(dispose).toHaveBeenCalledOnce();
  window.dispatchEvent(new Event('pagehide'));
});

test('ignores a session check completed after the document was cached', async () => {
  document.body.innerHTML = liveMarkup({content: directContent('Global Workspace'), home: homeMarkup()});
  window.history.replaceState(null, '', '/workspace');
  let resolveOld;
  let resolveCurrent;
  const fetcher = vi.fn().mockImplementationOnce(() => new Promise((resolve) => { resolveOld = resolve }))
    .mockImplementationOnce(() => new Promise((resolve) => { resolveCurrent = resolve }));
  initExtensionWorkspace(fetcher);
  window.dispatchEvent(new Event('focus'));
  await vi.waitFor(() => expect(fetcher).toHaveBeenCalledTimes(1));
  const cached = new Event('pagehide');
  Object.defineProperty(cached, 'persisted', {value: true});
  window.dispatchEvent(cached);
  const restored = new Event('pageshow');
  Object.defineProperty(restored, 'persisted', {value: true});
  window.dispatchEvent(restored);
  await vi.waitFor(() => expect(fetcher).toHaveBeenCalledTimes(2));
  resolveOld({status: 204, headers: new Headers({'X-Extension-Session-Generation': 'session-b'})});
  await new Promise((resolve) => setTimeout(resolve, 0));
  expect(document.querySelector('.page-content').textContent).toBe('Global Workspace');
  resolveCurrent({status: 204, headers: new Headers({'X-Extension-Session-Generation': 'session-a'})});
  await new Promise((resolve) => setTimeout(resolve, 0));
  expect(document.querySelector('.page-content').textContent).toBe('Global Workspace');
  window.dispatchEvent(new Event('pagehide'));
});

// NFX-A01/H-0, proved by NFX-A02: both entry orders run the real transplant
// chain across the direct contribution shape and the plain native shape,
// keeping one retained owner while the live page config follows each page.
test('contribution -> native -> contribution refreshes live page config with one retained owner', async () => {
  const savedConfig = window.config;
  window.history.replaceState(null, '', '/workspace');
  document.body.innerHTML = liveMarkup({
    content: directContent('Global Workspace'),
    home: homeMarkup({panels: '<div id="notes-owner">notes</div>'}),
  });
  window.config = {appSubUrl: '', pageData: {extension: {id: 'example'}}};
  const owner = document.getElementById('notes-owner');
  const adminPageData = {adminUserListSearchForm: {SortType: 'name'}};
  const workspacePageData = {extension: {id: 'example'}};
  const fetcher = vi.fn(async (url) => navigationResponse(url.includes('/admin/users') ?
    nativeDocument({title: 'Users - Forgejo', pageData: adminPageData, content: plainAdminContent('<form id="user-list-search-form"></form>'), home: homeMarkup({hidden: true})}) :
    nativeDocument({title: 'Workspace', pageData: workspacePageData, content: directContent('Global Workspace'), home: homeMarkup()})));
  try {
    initExtensionWorkspace(fetcher);
    clickWorkspaceLink('/admin/users');
    await vi.waitFor(() => expect(window.location.pathname).toBe('/admin/users'));
    expect(document.title).toBe('Users - Forgejo');
    expect(document.querySelector('#user-list-search-form')).not.toBeNull();
    expect(window.config.pageData).toEqual(adminPageData);
    expect(document.getElementById('notes-owner')).toBe(owner);
    clickWorkspaceLink('/workspace');
    await vi.waitFor(() => expect(window.location.pathname).toBe('/workspace'));
    expect(document.title).toBe('Workspace');
    expect(window.config.pageData).toEqual(workspacePageData);
    expect(document.getElementById('notes-owner')).toBe(owner);
    expect(owner.isConnected).toBe(true);
  } finally {
    window.dispatchEvent(new Event('pagehide'));
    window.config = savedConfig;
  }
});

// region_adopt: plain native documents carry the hidden home, so native-first
// order transplants immediately without a full load. Documents with no home
// (login and other ineligible shapes) stay fully native.
test('hiding the home moves inside focus to content; showing never steals it', async () => {
  const savedConfig = window.config;
  window.history.replaceState(null, '', '/workspace');
  document.body.innerHTML = liveMarkup({
    content: directContent('Global Workspace'),
    home: homeMarkup({panels: '<button type="button" id="panel-action">Act</button>'}),
  });
  window.config = {appSubUrl: '', pageData: {extension: {id: 'example'}}};
  const adminPageData = {adminUserListSearchForm: {SortType: 'name'}};
  const workspacePageData = {extension: {id: 'example'}};
  const fetcher = vi.fn(async (url) => navigationResponse(url.includes('/admin/users') ?
    nativeDocument({title: 'Users', pageData: adminPageData, content: plainAdminContent('<form></form>'), home: homeMarkup({hidden: true})}) :
    nativeDocument({title: 'Workspace', pageData: workspacePageData, content: directContent('Global Workspace'), home: homeMarkup()})));
  try {
    initExtensionWorkspace(fetcher);
    document.getElementById('panel-action').focus();
    expect(document.activeElement.id).toBe('panel-action');
    clickWorkspaceLink('/admin/users');
    await vi.waitFor(() => expect(window.location.pathname).toBe('/admin/users'));
    expect(document.activeElement).toBe(document.querySelector('.page-content'));
    expect(document.activeElement).not.toBe(document.body);
    clickWorkspaceLink('/workspace');
    await vi.waitFor(() => expect(window.location.pathname).toBe('/workspace'));
    expect(document.activeElement).toBe(document.querySelector('.page-content'));
  } finally {
    window.dispatchEvent(new Event('pagehide'));
    window.config = savedConfig;
  }
});

test('plain native documents transplant from the hidden home', async () => {
  const savedConfig = window.config;
  window.history.replaceState(null, '', '/admin/users');
  document.body.innerHTML = liveMarkup({
    content: plainAdminContent('<form id="user-list-search-form"></form>'),
    home: homeMarkup({hidden: true, panels: panelRootMarkup()}),
  });
  window.config = {appSubUrl: '', pageData: {adminUserListSearchForm: {SortType: 'name'}}};
  const home = document.querySelector('[data-extension-panels]');
  const workspacePageData = {extension: {id: 'example'}};
  const fetcher = vi.fn(async () => navigationResponse(nativeDocument({title: 'Workspace', pageData: workspacePageData,
    content: directContent(`Global Workspace${pageRootMarkup()}`), home: homeMarkup()})));
  try {
    initExtensionWorkspace(fetcher);
    expect(home.hasAttribute('hidden')).toBe(true);
    clickWorkspaceLink('/workspace');
    await vi.waitFor(() => expect(window.location.pathname).toBe('/workspace'));
    expect(fetcher).toHaveBeenCalledOnce();
    expect(document.title).toBe('Workspace');
    expect(window.config.pageData).toEqual(workspacePageData);
    expect(document.querySelector('[data-extension-panels]')).toBe(home);
    expect(home.hasAttribute('hidden')).toBe(false);
    expect(document.querySelector('.page-content').textContent).toContain('Global Workspace');
  } finally {
    window.dispatchEvent(new Event('pagehide'));
    window.config = savedConfig;
  }
});

test('dashboard transplants retain the initial home without a full load', async () => {
  const savedConfig = window.config;
  window.history.replaceState(null, '', '/workspace');
  document.body.innerHTML = liveMarkup({
    content: directContent(`Global Workspace${pageRootMarkup()}`),
    home: homeMarkup({panels: '<div id="notes-owner">notes</div>'}),
    navbar: '<a class="item" href="/admin">Admin</a><a class="item active" aria-current="page" href="/workspace">Workspace</a>',
  });
  window.config = {appSubUrl: '', pageData: {extension: {id: 'example'}}};
  const owner = document.getElementById('notes-owner');
  const home = document.querySelector('[data-extension-panels]');
  const fetcher = vi.fn(async (url) => {
    if (url.includes('/admin')) {
      return navigationResponse(nativeDocument({title: 'Dashboard - Forgejo', pageData: {}, content: plainAdminContent('<div>Dashboard</div>'), home: homeMarkup({hidden: true})}));
    }
    return navigationResponse(nativeDocument({title: 'Workspace', pageData: {extension: {id: 'example'}}, content: directContent(`Global Workspace${pageRootMarkup()}`), home: homeMarkup()}));
  });
  const assign = vi.spyOn(window.location, 'assign').mockImplementation(() => {});
  try {
    initExtensionWorkspace(fetcher);
    clickWorkspaceLink('/admin');
    await vi.waitFor(() => expect(window.location.pathname).toBe('/admin'));
    expect(document.title).toBe('Dashboard - Forgejo');
    expect(document.querySelector('[data-extension-panels]')).toBe(home);
    expect(home.hasAttribute('hidden')).toBe(true);
    expect(document.getElementById('notes-owner')).toBe(owner);
    expect(assign).not.toHaveBeenCalled();
  } finally {
    assign.mockRestore();
    window.dispatchEvent(new Event('pagehide'));
    window.config = savedConfig;
  }
});

test('push transplant snapshots outgoing scroll before the content swap', async () => {
  const savedConfig = window.config;
  window.history.replaceState(null, '', '/workspace');
  document.body.innerHTML = liveMarkup({
    content: directContent(`Global Workspace${pageRootMarkup()}`),
    home: homeMarkup({panels: '<div id="notes-owner">notes</div>'}),
  });
  window.config = {appSubUrl: '', pageData: {extension: {id: 'example'}}};
  const fetcher = vi.fn(async () => navigationResponse(nativeDocument({title: 'User accounts - Forgejo', pageData: {adminUserListSearchForm: {}}, content: plainAdminContent('<form id="user-list-search-form"></form>'), home: homeMarkup({hidden: true})})));
  // Simulate tall->short clamping: scroll reads 800 while the outgoing
  // tall content is live and 0 once the short entered content swaps in.
  Object.defineProperty(window, 'scrollY', {configurable: true, get: () => (document.querySelector('#user-list-search-form') ? 0 : 800)});
  const replaceState = vi.spyOn(window.history, 'replaceState');
  try {
    initExtensionWorkspace(fetcher);
    clickWorkspaceLink('/admin/users');
    await vi.waitFor(() => expect(window.location.pathname).toBe('/admin/users'));
    expect(replaceState).toHaveBeenCalledWith(expect.objectContaining({scrollY: 800}), '', expect.stringContaining('/workspace'));
    expect(window.history.state.scrollY).toBe(0);
  } finally {
    replaceState.mockRestore();
    delete window.scrollY;
    window.dispatchEvent(new Event('pagehide'));
    window.config = savedConfig;
  }
});

test('a page-only manifest mounts its entered page after native-first boot', async () => {
  // Blocker 1: with zero panel roots the eligible home is empty on native
  // boot, but the host must still listen so the transplanted page mounts.
  const savedConfig = window.config;
  window.history.replaceState(null, '', '/admin/users');
  document.body.innerHTML = liveMarkup({
    content: plainAdminContent('<form id="user-list-search-form"></form>'),
    home: homeMarkup({hidden: true}),
  });
  window.config = {appSubUrl: '', pageData: {adminUserListSearchForm: {SortType: 'name'}}};
  expect(document.querySelector('[data-extension-page],[data-extension-panel]')).toBeNull();
  const importer = vi.fn(async () => ({mount: () => ({dispose: () => {}})}));
  const workspacePageData = {extension: {id: 'example'}};
  const fetcher = vi.fn(async () => navigationResponse(nativeDocument({title: 'Workspace', pageData: workspacePageData,
    content: directContent(`Global Workspace${pageRootMarkup()}`), home: homeMarkup()})));
  try {
    initExtensionPages(importer);
    initExtensionWorkspace(fetcher);
    expect(importer).not.toHaveBeenCalled();
    clickWorkspaceLink('/workspace');
    await vi.waitFor(() => expect(window.location.pathname).toBe('/workspace'));
    await vi.waitFor(() => expect(importer).toHaveBeenCalledOnce());
    expect(document.querySelector('.page-content [data-extension-page]').hasAttribute('data-extension-mounted')).toBe(true);
  } finally {
    window.dispatchEvent(new Event('pagehide'));
    window.config = savedConfig;
  }
});

test('documents without a home stay fully native', async () => {
  const savedConfig = window.config;
  window.history.replaceState(null, '', '/admin/users');
  document.body.innerHTML = '<nav id="navbar"></nav><div class="full height"><div role="main" class="page-content admin"><div class="ui container flex-container"><div class="flex-container-main"><form id="user-list-search-form"></form></div></div></div></div><footer></footer>';
  window.config = {appSubUrl: '', pageData: {adminUserListSearchForm: {SortType: 'name'}}};
  const fetcher = vi.fn();
  try {
    initExtensionWorkspace(fetcher);
    expect(document.querySelector('[data-extension-panels]')).toBeNull();
    const link = document.createElement('a');
    link.href = '/workspace';
    document.body.append(link);
    const event = new MouseEvent('click', {bubbles: true, cancelable: true});
    link.dispatchEvent(event);
    expect(event.defaultPrevented).toBe(false);
    expect(fetcher).not.toHaveBeenCalled();
    link.remove();
  } finally {
    window.dispatchEvent(new Event('pagehide'));
    window.config = savedConfig;
  }
});

test('retires the document on a foreign generation even if native cleanup throws', async () => {
  const savedConfig = window.config;
  window.history.replaceState(null, '', '/workspace');
  document.body.innerHTML = liveMarkup({
    content: directContent(`Global Workspace${pageRootMarkup()}`),
    home: homeMarkup(),
  });
  window.config = {appSubUrl: '', pageData: {extension: {id: 'example'}}};
  const content = document.querySelector('.page-content');
  const home = document.querySelector('[data-extension-panels]');
  const consumerRetire = vi.fn();
  initExtensionPages(async () => ({mount: () => ({retire: consumerRetire})}));
  await new Promise((resolve) => setTimeout(resolve, 0));
  const retired = [];
  let checkCleanup = true;
  const cleanupError = new Error('native cleanup failed');
  registerPageFeature('a02-invalidation-cleanup', {
    init: () => {},
    retire: () => { if (checkCleanup) retired.push('cleanup'); },
  });
  registerPageFeature('a02-invalidation-failure', {
    init: () => {},
    retire: () => {
      if (!checkCleanup) return;
      expect(content.style.display).toBe('none');
      expect(home.hasAttribute('hidden')).toBe(true);
      expect(home.hasAttribute('inert')).toBe(true);
      expect(content.textContent).toContain('Global Workspace');
      retired.push('throw');
      throw cleanupError;
    },
  });
  initPageFeatures();
  const before = currentPageGeneration();
  const report = vi.spyOn(console, 'error').mockImplementation(() => {});
  const fetcher = vi.fn(async () => navigationResponse(
    nativeDocument({title: 'Workspace', pageData: {extension: {id: 'example'}},
      content: directContent(`Foreign${pageRootMarkup({generation: 'session-b'})}`), home: homeMarkup({generation: 'session-b'})}),
    'session-b'));
  const invalidated = vi.fn();
  window.addEventListener('extension-workspace-invalidated', invalidated);
  try {
    initExtensionWorkspace(fetcher);
    clickWorkspaceLink('/workspace?view=all');
    await vi.waitFor(() => expect(document.querySelector('.page-content').textContent).toContain('Your session changed'));
    expect(document.querySelector('[data-extension-page]')).toBeNull();
    expect(document.querySelector('[data-extension-panels]').hasAttribute('hidden')).toBe(true);
    expect(invalidated).toHaveBeenCalledOnce();
    expect(retired).toEqual(['throw', 'cleanup']);
    expect(currentPageGeneration()).toBe(before + 1);
    expect(report).toHaveBeenCalledWith('Could not retire native page features', cleanupError);
    expect(consumerRetire).toHaveBeenCalledOnce();
    expect(content.style.display).toBe('');
    clickWorkspaceLink('/workspace?view=again');
    await new Promise((resolve) => setTimeout(resolve, 0));
    expect(invalidated).toHaveBeenCalledOnce();
    expect(consumerRetire).toHaveBeenCalledOnce();
    expect(window.config.pageData).toEqual({});
  } finally {
    checkCleanup = false;
    retirePageFeatures();
    report.mockRestore();
    window.removeEventListener('extension-workspace-invalidated', invalidated);
    window.dispatchEvent(new Event('pagehide'));
    window.config = savedConfig;
  }
});

test('transplants same-origin GET form submits and leaves POST forms native', async () => {
  const savedConfig = window.config;
  window.history.replaceState(null, '', '/workspace');
  document.body.innerHTML = liveMarkup({
    content: directContent('<form id="user-list-search-form" method="get" action="/admin/users"><input type="search" name="q" value="alice"><button type="submit" name="sort" value="email">Sort</button></form>'),
    home: homeMarkup(),
  });
  window.config = {appSubUrl: '', pageData: {extension: {id: 'example'}}};
  const adminPageData = {adminUserListSearchForm: {SortType: 'email'}};
  const fetcher = vi.fn(async () => navigationResponse(nativeDocument({title: 'Users', pageData: adminPageData,
    content: plainAdminContent('<form id="user-list-search-form"></form>'), home: homeMarkup({hidden: true})})));
  try {
    initExtensionWorkspace(fetcher);
    document.querySelector('#user-list-search-form').requestSubmit(document.querySelector('button[name="sort"]'));
    await vi.waitFor(() => expect(window.location.pathname + window.location.search).toBe('/admin/users?q=alice&sort=email'));
    expect(fetcher).toHaveBeenCalledOnce();
    expect(window.config.pageData).toEqual(adminPageData);
    const post = document.createElement('form');
    post.method = 'post';
    post.action = '/admin/users';
    document.body.append(post);
    const event = new Event('submit', {bubbles: true, cancelable: true});
    post.dispatchEvent(event);
    expect(event.defaultPrevented).toBe(false);
    expect(fetcher).toHaveBeenCalledOnce();
    post.remove();
  } finally {
    window.dispatchEvent(new Event('pagehide'));
    window.config = savedConfig;
  }
});

test('brackets transplants with retire, config refresh and init in order', async () => {
  const savedConfig = window.config;
  window.history.replaceState(null, '', '/workspace');
  document.body.innerHTML = liveMarkup({content: directContent('Global Workspace'), home: homeMarkup()});
  window.config = {appSubUrl: '', pageData: {extension: {id: 'example'}}};
  const order = [];
  let observedConfig = null;
  let observedAddress;
  registerPageFeature('a02-bracketing-probe', {
    init: () => {
      order.push('init');
      observedConfig = window.config.pageData;
      observedAddress = window.location.pathname + window.location.search;
    },
    retire: () => {
      order.push('retire');
    },
  });
  const adminPageData = {adminUserListSearchForm: {SortType: 'name'}};
  const workspacePageData = {extension: {id: 'example'}};
  const fetcher = vi.fn(async (url) => navigationResponse(url.includes('/admin/users') ?
    nativeDocument({title: 'Users', pageData: adminPageData, content: plainAdminContent('<form></form>'), home: homeMarkup({hidden: true})}) :
    nativeDocument({title: 'Workspace', pageData: workspacePageData, content: directContent('Global Workspace'), home: homeMarkup()})));
  try {
    initExtensionWorkspace(fetcher);
    clickWorkspaceLink('/admin/users?sort=name');
    await vi.waitFor(() => expect(window.location.pathname).toBe('/admin/users'));
    expect(order).toEqual(['init']);
    expect(observedConfig).toEqual(adminPageData);
    expect(observedAddress).toBe('/admin/users?sort=name');
    clickWorkspaceLink('/workspace');
    await vi.waitFor(() => expect(window.location.pathname).toBe('/workspace'));
    expect(order).toEqual(['init', 'retire', 'init']);
  } finally {
    retirePageFeatures();
    window.dispatchEvent(new Event('pagehide'));
    window.config = savedConfig;
  }
});

// NFX-B05 dirty-form departure guard: areYouSure/beforeunload protects full
// loads, but in-shell transplants replace .page-content without unloading.
// The host reuses the mature .dirty/.dirty-file markers with native confirm,
// without dispatching global beforeunload handlers (stopwatch/notification
// close workers there). Happy-dom lacks :input so tests type then apply the
// mature marker directly; a real browser marks .dirty on typing.
function typeNativeDraft(textarea, value, form, dirtyClass = 'dirty') {
  textarea.value = value;
  textarea.dispatchEvent(new Event('input', {bubbles: true}));
  form.classList.add(dirtyClass);
}

function stubConfirm(impl) {
  const saved = window.confirm;
  window.confirm = vi.fn(impl);
  return () => {
    if (saved === undefined) delete window.confirm;
    else window.confirm = saved;
  };
}

test('dirty comment link refuses without fetch and accepts in-shell with warning', async () => {
  const savedConfig = window.config;
  window.history.replaceState(null, '', '/team/repo/issues/7');
  document.body.innerHTML = liveMarkup({
    content: directContent('<form class="comment form"><textarea name="content">initial</textarea></form>'),
    home: homeMarkup({hidden: true}),
  });
  window.config = {appSubUrl: '', pageData: {}};
  const form = document.querySelector('form.comment.form');
  const textarea = form.querySelector('textarea');
  typeNativeDraft(textarea, 'typed draft content', form);
  expect(form.classList.contains('dirty')).toBe(true);
  const beforeunload = vi.fn();
  window.addEventListener('beforeunload', beforeunload);
  const fetcher = vi.fn(async () => navigationResponse(nativeDocument({title: 'Users', pageData: {},
    content: plainAdminContent('<form id="user-list-search-form"></form>'), home: homeMarkup({hidden: true})})));
  const assign = vi.spyOn(window.location, 'assign').mockImplementation(() => {});
  const restoreRefuse = stubConfirm(() => false);
  try {
    initExtensionWorkspace(fetcher);
    clickWorkspaceLink('/admin/users');
    await new Promise((resolve) => setTimeout(resolve, 200));
    expect(window.confirm).toHaveBeenCalledExactlyOnceWith('You have unsaved changes!');
    expect(fetcher).not.toHaveBeenCalled();
    expect(assign).not.toHaveBeenCalled();
    expect(beforeunload).not.toHaveBeenCalled();
    expect(window.location.pathname).toBe('/team/repo/issues/7');
    expect(textarea.isConnected).toBe(true);
    expect(textarea.value).toBe('typed draft content');
    expect(form.classList.contains('dirty')).toBe(true);
  } finally {
    restoreRefuse();
    window.removeEventListener('beforeunload', beforeunload);
    window.dispatchEvent(new Event('pagehide'));
    window.config = savedConfig;
    assign.mockRestore();
  }

  window.history.replaceState(null, '', '/team/repo/issues/7');
  document.body.innerHTML = liveMarkup({
    content: directContent('<form class="comment form"><textarea name="content">initial</textarea></form>'),
    home: homeMarkup({hidden: true}),
  });
  window.config = {appSubUrl: '', pageData: {}};
  const form2 = document.querySelector('form.comment.form');
  typeNativeDraft(form2.querySelector('textarea'), 'typed draft content', form2);
  const fetcher2 = vi.fn(async () => navigationResponse(nativeDocument({title: 'Users', pageData: {},
    content: plainAdminContent('<form id="user-list-search-form"></form>'), home: homeMarkup({hidden: true})})));
  const assign2 = vi.spyOn(window.location, 'assign').mockImplementation(() => {});
  const restoreAccept = stubConfirm(() => true);
  try {
    initExtensionWorkspace(fetcher2);
    clickWorkspaceLink('/admin/users');
    await vi.waitFor(() => expect(window.location.pathname).toBe('/admin/users'));
    expect(window.confirm).toHaveBeenCalledOnce();
    expect(fetcher2).toHaveBeenCalledOnce();
    expect(assign2).not.toHaveBeenCalled();
    expect(document.querySelector('#user-list-search-form')).not.toBeNull();
  } finally {
    restoreAccept();
    window.dispatchEvent(new Event('pagehide'));
    window.config = savedConfig;
    assign2.mockRestore();
  }
});

test('editor dirty-file link refuses and preserves the draft', async () => {
  const savedConfig = window.config;
  window.history.replaceState(null, '', '/team/repo/_edit/main/README.md');
  document.body.innerHTML = liveMarkup({
    content: '<div role="main" class="page-content repository"><div class="ui container"><form class="ui edit form"><textarea id="edit_area" name="content">initial</textarea></form></div></div>',
    home: homeMarkup({hidden: true}),
  });
  window.config = {appSubUrl: '', pageData: {}};
  const form = document.querySelector('form.ui.edit.form');
  const textarea = form.querySelector('textarea');
  typeNativeDraft(textarea, 'edited file content', form, 'dirty-file');
  const fetcher = vi.fn(async () => navigationResponse(nativeDocument({title: 'Users', pageData: {},
    content: plainAdminContent('Users'), home: homeMarkup({hidden: true})})));
  const restore = stubConfirm(() => false);
  try {
    initExtensionWorkspace(fetcher);
    clickWorkspaceLink('/admin/users');
    await new Promise((resolve) => setTimeout(resolve, 200));
    expect(window.confirm).toHaveBeenCalledOnce();
    expect(fetcher).not.toHaveBeenCalled();
    expect(window.location.pathname).toBe('/team/repo/_edit/main/README.md');
    expect(textarea.isConnected).toBe(true);
    expect(textarea.value).toBe('edited file content');
    expect(form.classList.contains('dirty-file')).toBe(true);
  } finally {
    restore();
    window.dispatchEvent(new Event('pagehide'));
    window.config = savedConfig;
  }
});

test('GET submit with another dirty form refuses then accepts; submitting form alone never prompts', async () => {
  const savedConfig = window.config;
  window.history.replaceState(null, '', '/team/repo/issues/7');
  document.body.innerHTML = liveMarkup({
    content: directContent('<form class="comment form"><textarea name="content">initial</textarea></form><form id="search" method="get" action="/admin/users"><input type="search" name="q" value="alice"></form>'),
    home: homeMarkup({hidden: true}),
  });
  window.config = {appSubUrl: '', pageData: {}};
  const comment = document.querySelector('form.comment.form');
  typeNativeDraft(comment.querySelector('textarea'), 'comment draft', comment);
  const fetcher = vi.fn(async () => navigationResponse(nativeDocument({title: 'Users', pageData: {},
    content: plainAdminContent('Users'), home: homeMarkup({hidden: true})})));
  const restoreRefuse = stubConfirm(() => false);
  try {
    initExtensionWorkspace(fetcher);
    document.querySelector('#search').requestSubmit();
    await new Promise((resolve) => setTimeout(resolve, 200));
    expect(window.confirm).toHaveBeenCalledOnce();
    expect(fetcher).not.toHaveBeenCalled();
    expect(window.location.pathname).toBe('/team/repo/issues/7');
    expect(comment.querySelector('textarea').value).toBe('comment draft');
  } finally {
    restoreRefuse();
    window.dispatchEvent(new Event('pagehide'));
    window.config = savedConfig;
  }

  window.history.replaceState(null, '', '/team/repo/issues/7');
  document.body.innerHTML = liveMarkup({
    content: directContent('<form id="search" method="get" action="/admin/users"><input type="search" name="q" value="alice"></form>'),
    home: homeMarkup({hidden: true}),
  });
  window.config = {appSubUrl: '', pageData: {}};
  const search = document.querySelector('#search');
  search.classList.add('dirty');
  const fetcher2 = vi.fn(async () => navigationResponse(nativeDocument({title: 'Users', pageData: {},
    content: plainAdminContent('Users'), home: homeMarkup({hidden: true})})));
  const restoreAccept = stubConfirm(() => { throw new Error('submitting GET must not prompt') });
  try {
    initExtensionWorkspace(fetcher2);
    search.requestSubmit();
    await vi.waitFor(() => expect(window.location.pathname).toBe('/admin/users'));
    expect(fetcher2).toHaveBeenCalledOnce();
    expect(window.location.search).toBe('?q=alice');
  } finally {
    restoreAccept();
    window.dispatchEvent(new Event('pagehide'));
    window.config = savedConfig;
  }
});

test('programmatic push refuses without fetch and accepts in-shell', async () => {
  const savedConfig = window.config;
  window.history.replaceState(null, '', '/team/repo/issues/7');
  document.body.innerHTML = liveMarkup({
    content: directContent('<form class="comment form"><textarea name="content">initial</textarea></form>'),
    home: homeMarkup({hidden: true}),
  });
  window.config = {appSubUrl: '', pageData: {}};
  const form = document.querySelector('form.comment.form');
  typeNativeDraft(form.querySelector('textarea'), 'push draft', form);
  const fetcher = vi.fn(async () => navigationResponse(nativeDocument({title: 'Users', pageData: {},
    content: plainAdminContent('Users'), home: homeMarkup({hidden: true})})));
  const restoreRefuse = stubConfirm(() => false);
  try {
    initExtensionWorkspace(fetcher);
    navigateNative('/admin/users', {replace: false});
    await new Promise((resolve) => setTimeout(resolve, 200));
    expect(window.confirm).toHaveBeenCalledOnce();
    expect(fetcher).not.toHaveBeenCalled();
    expect(window.location.pathname).toBe('/team/repo/issues/7');
    expect(form.querySelector('textarea').value).toBe('push draft');
  } finally {
    restoreRefuse();
    window.dispatchEvent(new Event('pagehide'));
    window.config = savedConfig;
  }

  window.history.replaceState(null, '', '/team/repo/issues/7');
  document.body.innerHTML = liveMarkup({
    content: directContent('<form class="comment form"><textarea name="content">initial</textarea></form>'),
    home: homeMarkup({hidden: true}),
  });
  window.config = {appSubUrl: '', pageData: {}};
  typeNativeDraft(document.querySelector('textarea'), 'push draft', document.querySelector('form'));
  const fetcher2 = vi.fn(async () => navigationResponse(nativeDocument({title: 'Users', pageData: {},
    content: plainAdminContent('Users'), home: homeMarkup({hidden: true})})));
  const restoreAccept = stubConfirm(() => true);
  try {
    initExtensionWorkspace(fetcher2);
    navigateNative('/admin/users', {replace: false});
    await vi.waitFor(() => expect(window.location.pathname).toBe('/admin/users'));
    expect(window.confirm).toHaveBeenCalledOnce();
    expect(fetcher2).toHaveBeenCalledOnce();
  } finally {
    restoreAccept();
    window.dispatchEvent(new Event('pagehide'));
    window.config = savedConfig;
  }
});

test('refused Back/Forward keep the real history positions and the forward tip', async () => {
  const savedConfig = window.config;
  const issuesDoc = () => nativeDocument({title: 'Issue', pageData: {},
    content: directContent('<form class="comment form"><textarea name="content">initial</textarea></form>'), home: homeMarkup({hidden: true})});
  const usersDoc = () => nativeDocument({title: 'Users', pageData: {},
    content: plainAdminContent('Users'), home: homeMarkup({hidden: true})});
  window.history.replaceState(null, '', '/team/repo');
  document.body.innerHTML = liveMarkup({
    content: directContent('Repo home'),
    home: homeMarkup({hidden: true}),
  });
  window.config = {appSubUrl: '', pageData: {}};
  const fetcher = vi.fn(async (url) => navigationResponse(url.includes('/admin/users') ? usersDoc() : issuesDoc()));
  let answer = false;
  const restore = stubConfirm(() => answer);
  try {
    initExtensionWorkspace(fetcher);
    // Real in-shell pushes build [repo, issues(dirty-to-be), users(tip)].
    clickWorkspaceLink('/team/repo/issues/7');
    await vi.waitFor(() => expect(window.location.pathname).toBe('/team/repo/issues/7'));
    clickWorkspaceLink('/admin/users');
    await vi.waitFor(() => expect(window.location.pathname).toBe('/admin/users'));
    const lengthAtTip = window.history.length;
    // Return to the middle entry and make it dirty there.
    window.history.back();
    await vi.waitFor(() => expect(document.querySelector('form.comment.form')).not.toBeNull());
    expect(window.location.pathname).toBe('/team/repo/issues/7');
    const form = document.querySelector('form.comment.form');
    const textarea = form.querySelector('textarea');
    typeNativeDraft(textarea, 'traversal draft', form);
    fetcher.mockClear();
    // Refuse Back: original middle position, draft and forward tip survive.
    window.history.back();
    await vi.waitFor(() => expect(window.location.pathname).toBe('/team/repo/issues/7'));
    await new Promise((resolve) => setTimeout(resolve, 50));
    expect(window.location.pathname).toBe('/team/repo/issues/7');
    expect(window.history.length).toBe(lengthAtTip);
    expect(fetcher).not.toHaveBeenCalled();
    expect(textarea.isConnected).toBe(true);
    expect(textarea.value).toBe('traversal draft');
    // Refuse Forward: still on the dirty middle entry, tip not truncated.
    window.history.forward();
    await vi.waitFor(() => expect(window.location.pathname).toBe('/team/repo/issues/7'));
    await new Promise((resolve) => setTimeout(resolve, 50));
    expect(window.location.pathname).toBe('/team/repo/issues/7');
    expect(window.history.length).toBe(lengthAtTip);
    expect(textarea.value).toBe('traversal draft');
    // Accept Forward: the original tip is still reachable.
    answer = true;
    window.history.forward();
    await vi.waitFor(() => expect(document.querySelector('.page-content').textContent).toContain('Users'));
    expect(window.location.pathname).toBe('/admin/users');
    expect(window.history.length).toBe(lengthAtTip);
  } finally {
    restore();
    window.dispatchEvent(new Event('pagehide'));
    window.config = savedConfig;
  }
});

test('accepted Back from a dirty page transplants the previous entry', async () => {
  const savedConfig = window.config;
  window.history.replaceState(null, '', '/team/repo');
  document.body.innerHTML = liveMarkup({
    content: directContent('Repo home'),
    home: homeMarkup({hidden: true}),
  });
  window.config = {appSubUrl: '', pageData: {}};
  const fetcher = vi.fn(async () => navigationResponse(nativeDocument({title: 'Issue', pageData: {},
    content: directContent('<form class="comment form"><textarea name="content">initial</textarea></form>'), home: homeMarkup({hidden: true})})));
  const restore = stubConfirm(() => true);
  try {
    initExtensionWorkspace(fetcher);
    clickWorkspaceLink('/team/repo/issues/7');
    await vi.waitFor(() => expect(window.location.pathname).toBe('/team/repo/issues/7'));
    const form = document.querySelector('form.comment.form');
    typeNativeDraft(form.querySelector('textarea'), 'leaving draft', form);
    window.history.back();
    await vi.waitFor(() => expect(window.location.pathname).toBe('/team/repo'));
    expect(window.confirm).toHaveBeenCalledOnce();
    expect(fetcher).toHaveBeenCalled();
  } finally {
    restore();
    window.dispatchEvent(new Event('pagehide'));
    window.config = savedConfig;
  }
});

test('native line/graph history entries survive a refused dirty traversal', async () => {
  const savedConfig = window.config;
  const codeDoc = () => nativeDocument({title: 'Code', pageData: {},
    content: directContent('<a href="/team/repo/issues">Issues</a><form class="comment form"><textarea name="content">initial</textarea></form>'), home: homeMarkup({hidden: true})});
  const issuesDoc = () => nativeDocument({title: 'Issues', pageData: {},
    content: directContent('<form class="comment form"><textarea name="content">initial</textarea></form>'), home: homeMarkup({hidden: true})});
  window.history.replaceState(null, '', '/workspace');
  document.body.innerHTML = liveMarkup({
    content: directContent('Global Workspace'),
    home: homeMarkup({hidden: true}),
  });
  window.config = {appSubUrl: '', pageData: {}};
  const fetcher = vi.fn(async (url) => navigationResponse(url.includes('/issues') ? issuesDoc() : codeDoc()));
  let answer = false;
  const restore = stubConfirm(() => answer);
  try {
    initExtensionWorkspace(fetcher);
    clickWorkspaceLink('/team/repo');
    await vi.waitFor(() => expect(window.location.pathname).toBe('/team/repo'));
    // Native feature writes, exactly as repo-code/repo-graph perform them.
    window.history.pushState(null, null, '#L12');
    window.history.pushState(null, null, '#L20');
    window.history.replaceState({}, '', '?mode=monochrome#L20');
    clickWorkspaceLink('/team/repo/issues');
    await vi.waitFor(() => expect(window.location.pathname).toBe('/team/repo/issues'));
    const lengthAtTip = window.history.length;
    const form = document.querySelector('form.comment.form');
    typeNativeDraft(form.querySelector('textarea'), 'tip draft', form);
    fetcher.mockClear();
    // Refuse Back across the native entries: exact tip position restored.
    window.history.back();
    await vi.waitFor(() => expect(window.location.pathname).toBe('/team/repo/issues'));
    await new Promise((resolve) => setTimeout(resolve, 50));
    expect(window.location.pathname).toBe('/team/repo/issues');
    expect(window.history.length).toBe(lengthAtTip);
    expect(form.querySelector('textarea').value).toBe('tip draft');
    // Accept Back: the native line entry is landed on, not overwritten.
    answer = true;
    window.history.back();
    await vi.waitFor(() => expect(window.location.hash).toBe('#L20'));
    expect(window.location.pathname).toBe('/team/repo');
    expect(window.location.search).toBe('?mode=monochrome');
    // Forward returns to the original tip.
    window.history.forward();
    await vi.waitFor(() => expect(window.location.pathname).toBe('/team/repo/issues'));
    expect(window.history.length).toBe(lengthAtTip);
  } finally {
    restore();
    window.dispatchEvent(new Event('pagehide'));
    window.config = savedConfig;
  }
});

test('repeating an unchanged native GET submission still performs the GET', async () => {
  const savedConfig = window.config;
  window.history.replaceState(null, '', '/admin/users?q=alice');
  document.body.innerHTML = liveMarkup({
    content: plainAdminContent('<form id="search" method="get" action="/admin/users"><input type="search" name="q" value="alice"></form>'),
    home: homeMarkup({hidden: true}),
  });
  window.config = {appSubUrl: '', pageData: {}};
  const fetcher = vi.fn(async () => navigationResponse(nativeDocument({title: 'Users', pageData: {},
    content: plainAdminContent('<form id="search" method="get" action="/admin/users"><input type="search" name="q" value="alice"></form>'), home: homeMarkup({hidden: true})})));
  const restore = stubConfirm(() => { throw new Error('clean GET must not prompt') });
  try {
    initExtensionWorkspace(fetcher);
    document.querySelector('#search').requestSubmit();
    await vi.waitFor(() => expect(fetcher).toHaveBeenCalledTimes(1));
    await vi.waitFor(() => expect(document.querySelector('#search')).not.toBeNull());
    document.querySelector('#search').requestSubmit();
    await vi.waitFor(() => expect(fetcher).toHaveBeenCalledTimes(2));
    expect(window.location.pathname + window.location.search).toBe('/admin/users?q=alice');
  } finally {
    restore();
    window.dispatchEvent(new Event('pagehide'));
    window.config = savedConfig;
  }
});

test('already-current fragment clicks preserve a dirty draft without prompting or fetching', async () => {
  const savedConfig = window.config;
  window.history.replaceState(null, '', '/team/repo/issues/7#note');
  document.body.innerHTML = liveMarkup({
    content: directContent('<form class="comment form"><textarea name="content">initial</textarea></form><a href="#note">Note</a><h2 id="user-content-note">Note</h2>'),
    home: homeMarkup({hidden: true}),
  });
  window.config = {appSubUrl: '', pageData: {}};
  const form = document.querySelector('form.comment.form');
  const textarea = form.querySelector('textarea');
  typeNativeDraft(textarea, 'current fragment draft', form);
  const fetcher = vi.fn();
  const restore = stubConfirm(() => { throw new Error('already-current fragment must not prompt') });
  try {
    initExtensionWorkspace(fetcher);
    const lengthBefore = window.history.length;
    document.querySelector('a[href="#note"]').click();
    document.querySelector('a[href="#note"]').click();
    await new Promise((resolve) => setTimeout(resolve, 50));
    expect(window.location.hash).toBe('#note');
    expect(window.history.length).toBe(lengthBefore);
    expect(document.querySelector('textarea')).toBe(textarea);
    expect(textarea.value).toBe('current fragment draft');
    expect(fetcher).not.toHaveBeenCalled();
  } finally {
    restore();
    window.dispatchEvent(new Event('pagehide'));
    window.config = savedConfig;
  }
});

test('same-hash navigation with a dirty form preserves the draft without prompting', async () => {
  const savedConfig = window.config;
  window.history.replaceState(null, '', '/team/repo/issues/7');
  document.body.innerHTML = liveMarkup({
    content: directContent('<form class="comment form"><textarea name="content">initial</textarea></form><a href="#note">Note</a><h2 id="user-content-note">Note</h2>'),
    home: homeMarkup({hidden: true}),
  });
  window.config = {appSubUrl: '', pageData: {}};
  const form = document.querySelector('form.comment.form');
  const textarea = form.querySelector('textarea');
  typeNativeDraft(textarea, 'hash draft', form);
  const fetcher = vi.fn();
  const restore = stubConfirm(() => { throw new Error('hash-only must not prompt') });
  try {
    initExtensionWorkspace(fetcher);
    document.querySelector('a[href="#note"]').click();
    expect(window.location.hash).toBe('#note');
    expect(document.querySelector('textarea')).toBe(textarea);
    expect(textarea.value).toBe('hash draft');
    expect(form.classList.contains('dirty')).toBe(true);
    expect(fetcher).not.toHaveBeenCalled();
  } finally {
    restore();
    window.dispatchEvent(new Event('pagehide'));
    window.config = savedConfig;
  }
});

test('clean link navigation never prompts', async () => {
  const savedConfig = window.config;
  window.history.replaceState(null, '', '/team/repo/issues/7');
  document.body.innerHTML = liveMarkup({
    content: directContent('<form class="comment form"><textarea name="content">initial</textarea></form>'),
    home: homeMarkup({hidden: true}),
  });
  window.config = {appSubUrl: '', pageData: {}};
  const fetcher = vi.fn(async () => navigationResponse(nativeDocument({title: 'Users', pageData: {},
    content: plainAdminContent('Users'), home: homeMarkup({hidden: true})})));
  const restore = stubConfirm(() => { throw new Error('clean navigation must not prompt') });
  try {
    initExtensionWorkspace(fetcher);
    clickWorkspaceLink('/admin/users');
    await vi.waitFor(() => expect(window.location.pathname).toBe('/admin/users'));
    expect(fetcher).toHaveBeenCalledOnce();
  } finally {
    restore();
    window.dispatchEvent(new Event('pagehide'));
    window.config = savedConfig;
  }
});

test('a repository feed at /workspace keeps the workspace hidden across entry and navigation', async () => {
  const savedConfig = window.config;
  window.history.replaceState(null, '', '/team/repo/workspace');
  document.body.innerHTML = liveMarkup({content: directContent('<h1>Project feed</h1>'), home: homeMarkup({hidden: true, panels: '<div id="retained-workspace">Workspace</div>'})});
  window.config = {appSubUrl: '', pageData: {extension: {id: 'example', pageID: 'repository-feed'}}};
  const owner = document.getElementById('retained-workspace');
  const fetcher = vi.fn(async (url) => navigationResponse(nativeDocument({
    title: url.includes('/team/repo/') ? 'Project feed' : 'Workspace',
    pageData: {extension: {id: 'example'}},
    content: directContent(url.includes('/team/repo/') ? '<h1>Project feed</h1>' : 'Workspace'),
    home: homeMarkup({hidden: url.includes('/team/repo/')}),
  })));
  try {
    initExtensionWorkspace(fetcher);
    const home = document.querySelector('[data-extension-panels]');
    expect(home.hasAttribute('hidden')).toBe(true);
    expect(home.hasAttribute('inert')).toBe(true);
    clickWorkspaceLink('/workspace');
    await vi.waitFor(() => expect(window.location.pathname).toBe('/workspace'));
    expect(home.hasAttribute('hidden')).toBe(false);
    expect(document.getElementById('retained-workspace')).toBe(owner);
    clickWorkspaceLink('/team/repo/workspace');
    await vi.waitFor(() => expect(window.location.pathname).toBe('/team/repo/workspace'));
    expect(home.hasAttribute('hidden')).toBe(true);
    expect(home.hasAttribute('inert')).toBe(true);
    expect(document.getElementById('retained-workspace')).toBe(owner);
  } finally {
    window.dispatchEvent(new Event('pagehide'));
    window.config = savedConfig;
  }
});

test('a hidden shared Workspace loads on first open and reuses its mounted owner', async () => {
  const savedConfig = window.config;
  window.history.replaceState(null, '', '/team/repo/workspace');
  const panel = panelRootMarkup({panelId: 'workspace', ownerId: 'workspace', apiBase: '/-/native/pages/example/spaces/api/'});
  document.body.innerHTML = liveMarkup({content: directContent('Project feed'), home: homeMarkup({hidden: true, panels: panel})});
  window.config = {appSubUrl: '', pageData: {}};
  const mount = vi.fn(() => ({update: vi.fn(), dispose: vi.fn()}));
  const importer = vi.fn(async () => ({mount}));
  const fetcher = vi.fn(async (url) => navigationResponse(nativeDocument({
    title: 'Page', pageData: {}, content: directContent('Page'),
    home: homeMarkup({hidden: url.includes('/team/repo/'), panels: panel}),
  })));
  try {
    initExtensionPages(importer);
    initExtensionWorkspace(fetcher);
    await new Promise((resolve) => setTimeout(resolve, 0));
    expect(importer).not.toHaveBeenCalled();
    expect(mount).not.toHaveBeenCalled();
    expect(document.querySelector('[data-extension-panel]').hasAttribute('data-extension-mounted')).toBe(false);
    clickWorkspaceLink('/workspace');
    await vi.waitFor(() => expect(mount).toHaveBeenCalledOnce());
    const root = document.querySelector('[data-extension-panel]');
    clickWorkspaceLink('/team/repo/workspace');
    await vi.waitFor(() => expect(window.location.pathname).toBe('/team/repo/workspace'));
    expect(document.querySelector('[data-extension-panels]').hasAttribute('hidden')).toBe(true);
    clickWorkspaceLink('/workspace');
    await vi.waitFor(() => expect(window.location.pathname).toBe('/workspace'));
    expect(document.querySelector('[data-extension-panel]')).toBe(root);
    expect(importer).toHaveBeenCalledOnce();
    expect(mount).toHaveBeenCalledOnce();
  } finally {
    window.dispatchEvent(new Event('pagehide'));
    window.config = savedConfig;
  }
});
