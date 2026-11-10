import {vi} from 'vitest';
import {initExtensionPages} from './extensions.js';

function contributionRoot({mode = 'page', extensionId = 'example', id = 'spaces', owner = '', prefix = '', entry = 'main.js', apiScope = '', patterns, version = '1', repositoryId} = {}) {
  const root = document.createElement('div');
  root.setAttribute(`data-extension-${mode}`, '');
  Object.assign(root.dataset, {
    appSubUrl: prefix,
    extensionId,
    extensionEntry: `${prefix}/-/native/assets/${extensionId}/${entry}`,
    extensionAssetBase: `${prefix}/-/native/assets/${extensionId}/`,
    extensionApiBase: `${prefix}${apiScope}/-/native/${mode}s/${extensionId}/${id}/api/`,
    extensionSessionGeneration: 'session-a',
    extensionContractVersion: version,
  });
  root.dataset[mode === 'page' ? 'extensionPageId' : 'extensionPanelId'] = id;
  if (owner) root.dataset.extensionOwnerId = owner;
  if (mode === 'panel' && owner) root.dataset.extensionApiBase = `${prefix}/-/native/pages/${extensionId}/${owner}/api/`;
  if (patterns !== undefined) root.dataset.extensionDirectPatterns = JSON.stringify(patterns);
  if (repositoryId !== undefined) root.dataset.extensionRepositoryId = repositoryId;
  return root;
}

function panelsHome(roots = [], prefix = '') {
  const home = document.createElement('aside');
  home.setAttribute('data-extension-panels', '');
  home.dataset.appSubUrl = prefix;
  home.dataset.extensionSessionGeneration = 'session-a';
  home.append(...roots);
  return home;
}

function installDocument(pages = [], panels = [], prefix = '') {
  const content = document.createElement('main');
  content.className = 'page-content';
  content.append(...pages);
  const home = panelsHome(panels, prefix);
  document.body.replaceChildren(content, home);
  return {content, home};
}

function prepareNavigation(enteredHome) {
  window.dispatchEvent(new CustomEvent('extension-workspace-will-navigate', {
    detail: enteredHome ? {panelsHome: enteredHome} : {},
  }));
}

function presentRoute(route, facts = {}) {
  window.dispatchEvent(new CustomEvent('extension-workspace-navigation', {detail: {route, params: {}, ...facts}}));
}

function observeMounts({stream = false} = {}) {
  const mounts = [];
  const importer = vi.fn(async (entry) => ({mount(root, context) {
    const handle = {update: vi.fn(), suspend: vi.fn(), resume: vi.fn(), retire: vi.fn(), dispose: vi.fn()};
    if (stream) context.stream('events');
    mounts.push({root, context, handle, entry});
    return handle;
  }}));
  return {mounts, importer};
}

async function settledMounts(mounts, count) {
  await vi.waitFor(() => expect(mounts).toHaveLength(count));
  await new Promise((resolve) => setTimeout(resolve, 0));
}

afterEach(() => {
  window.dispatchEvent(new Event('pagehide'));
  document.body.replaceChildren();
  window.history.replaceState(null, '', '/');
  vi.unstubAllGlobals();
});

test.each([
  ['entry URL', {entry: 'next.js'}],
  ['API scope', {apiScope: '/user/settings'}],
  ['owner', {owner: 'another'}],
  ['app prefix and asset base', {prefix: '/forge'}],
])('a changed %s retires the same contribution instead of retaining its old code', async (_name, change) => {
  const sockets = [];
  vi.stubGlobal('WebSocket', class {
    close = vi.fn();
    constructor() { sockets.push(this) }
  });
  window.history.replaceState(null, '', '/applications');
  const original = contributionRoot();
  const {content} = installDocument([original]);
  const {mounts, importer} = observeMounts({stream: true});
  initExtensionPages(importer);
  await settledMounts(mounts, 1);
  const previous = mounts[0];
  prepareNavigation();
  const incoming = contributionRoot(change);
  content.replaceChildren(incoming);
  presentRoute('/applications?updated=1');
  await settledMounts(mounts, 2);
  expect(previous.handle.retire).toHaveBeenCalledExactlyOnceWith('contribution-unavailable');
  expect(previous.handle.dispose).toHaveBeenCalledOnce();
  expect(previous.handle.resume).not.toHaveBeenCalled();
  expect(original.isConnected).toBe(false);
  expect(content.firstChild).toBe(incoming);
  expect(mounts[1].root).toBe(incoming);
  expect(sockets[0].close).toHaveBeenCalledOnce();
  expect(sockets[1].close).not.toHaveBeenCalled();
  expect(() => previous.context.request('private')).toThrow('retired');
});

test('a display-only change and equivalent Unicode prefix reuse the admitted owner', async () => {
  window.history.replaceState(null, '', '/café/applications');
  const original = contributionRoot({prefix: '/café'});
  const {content} = installDocument([original], [], '/café');
  const {mounts, importer} = observeMounts();
  initExtensionPages(importer);
  await settledMounts(mounts, 1);
  prepareNavigation();
  const incoming = contributionRoot({prefix: '/caf%C3%A9', repositoryId: '7'});
  content.replaceChildren(incoming);
  presentRoute('/caf%C3%A9/applications?view=all', {repositoryId: '7'});
  expect(content.firstChild).toBe(original);
  expect(original.dataset.extensionRepositoryId).toBe('7');
  expect(importer).toHaveBeenCalledOnce();
  expect(mounts[0].handle.retire).not.toHaveBeenCalled();
  expect(mounts[0].handle.resume).toHaveBeenCalledExactlyOnceWith({sessionGeneration: 'session-a'});
  expect(mounts[0].handle.update).toHaveBeenLastCalledWith(expect.objectContaining({mode: 'page', visible: true, repositoryId: '7'}));
});

test('disjoint shared owners present only their own non-workspace routes', async () => {
  window.history.replaceState(null, '', '/applications/requests/ABC');
  const applications = contributionRoot({mode: 'panel', id: 'apps-panel', owner: 'apps', patterns: ['/applications', '/applications/requests/{request}']});
  const tailnet = contributionRoot({mode: 'panel', id: 'tail-panel', owner: 'tail', patterns: ['/tailnet', '/tailnet/nodes/{node}']});
  const {home} = installDocument([], [applications, tailnet]);
  home.dataset.extensionSubroutes = JSON.stringify(['/applications/requests/{request}', '/tailnet/nodes/{node}', '/workspace']);
  const {mounts, importer} = observeMounts();
  initExtensionPages(importer);
  await settledMounts(mounts, 2);
  const apps = mounts.find((mount) => mount.context.ownerId === 'apps').handle;
  const tail = mounts.find((mount) => mount.context.ownerId === 'tail').handle;
  expect(apps.update).toHaveBeenCalledWith(expect.objectContaining({mode: 'page', visible: true}));
  presentRoute('/applications/requests/ABC');
  expect(apps.update).toHaveBeenLastCalledWith(expect.objectContaining({mode: 'page', visible: true}));
  expect(tail.update).toHaveBeenLastCalledWith(expect.objectContaining({mode: 'panel', visible: true}));
  presentRoute('/tailnet/nodes/XYZ');
  expect(apps.update).toHaveBeenLastCalledWith(expect.objectContaining({mode: 'panel', visible: true}));
  expect(tail.update).toHaveBeenLastCalledWith(expect.objectContaining({mode: 'page', visible: true}));
  presentRoute('/team/repo/workspace');
  expect(apps.update).toHaveBeenLastCalledWith(expect.objectContaining({mode: 'panel', visible: true}));
  expect(tail.update).toHaveBeenLastCalledWith(expect.objectContaining({mode: 'panel', visible: true}));
  expect(importer).toHaveBeenCalledTimes(2);
});

test('initial owner presentation uses the canonical Unicode deployment prefix', async () => {
  window.history.replaceState(null, '', '/café/applications/requests/ABC');
  const panel = contributionRoot({mode: 'panel', prefix: '/café', id: 'apps-panel', owner: 'apps', patterns: ['/applications/requests/{request}']});
  installDocument([], [panel], '/café');
  const {mounts, importer} = observeMounts();
  initExtensionPages(importer);
  await settledMounts(mounts, 1);
  expect(mounts[0].handle.update).toHaveBeenCalledExactlyOnceWith(expect.objectContaining({mode: 'page', visible: true}));
});

test('peer reconciliation refreshes owner patterns without replacing its lifetime', async () => {
  window.history.replaceState(null, '', '/applications');
  const panel = contributionRoot({mode: 'panel', id: 'apps-panel', owner: 'apps', patterns: ['/applications']});
  const {home} = installDocument([], [panel]);
  const {mounts, importer} = observeMounts();
  initExtensionPages(importer);
  await settledMounts(mounts, 1);
  const handle = mounts[0].handle;
  prepareNavigation(panelsHome([contributionRoot({mode: 'panel', id: 'apps-panel', owner: 'apps', patterns: ['/feed'], repositoryId: '7'})]));
  presentRoute('/applications', {repositoryId: '7'});
  expect(home.querySelector('[data-extension-panel]')).toBe(panel);
  expect(panel.dataset.extensionRepositoryId).toBe('7');
  expect(JSON.parse(panel.dataset.extensionDirectPatterns)).toEqual(['/feed']);
  expect(handle.update).toHaveBeenLastCalledWith(expect.objectContaining({mode: 'panel', visible: true}));
  presentRoute('/feed');
  expect(handle.update).toHaveBeenLastCalledWith(expect.objectContaining({mode: 'page', visible: true}));
  prepareNavigation(panelsHome([contributionRoot({mode: 'panel', id: 'apps-panel', owner: 'apps'})]));
  presentRoute('/feed');
  expect(panel.dataset.extensionDirectPatterns).toBeUndefined();
  expect(handle.update).toHaveBeenLastCalledWith(expect.objectContaining({mode: 'panel', visible: true}));
  expect(importer).toHaveBeenCalledOnce();
  expect(handle.retire).not.toHaveBeenCalled();
  expect(handle.dispose).not.toHaveBeenCalled();
});

test('an invalid panel cannot suppress a valid same-owner page', async () => {
  const page = contributionRoot({owner: 'shared'});
  const invalidPanel = contributionRoot({mode: 'panel', id: 'shared-panel', owner: 'shared', version: '2'});
  installDocument([page], [invalidPanel]);
  const {mounts, importer} = observeMounts();
  initExtensionPages(importer);
  await settledMounts(mounts, 1);
  expect(mounts[0].root).toBe(page);
  expect(mounts[0].context.pageId).toBe('spaces');
  expect(page.hasAttribute('data-extension-owner-presented')).toBe(false);
  expect(importer).toHaveBeenCalledOnce();
  expect(invalidPanel.textContent).toContain('unavailable');
});

test('another extension using the same owner label cannot suppress the page', async () => {
  const page = contributionRoot({owner: 'shared'});
  const otherPanel = contributionRoot({mode: 'panel', extensionId: 'other', id: 'shared-panel', owner: 'shared'});
  installDocument([page], [otherPanel]);
  const {mounts, importer} = observeMounts();
  initExtensionPages(importer);
  await settledMounts(mounts, 2);
  expect(mounts.map((mount) => mount.root)).toContain(page);
  expect(mounts.map((mount) => mount.root)).toContain(otherPanel);
  expect(page.hasAttribute('data-extension-owner-presented')).toBe(false);
  expect(importer).toHaveBeenCalledTimes(2);
});
