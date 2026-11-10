import {vi} from 'vitest';
import {initExtensionPages, initExtensionWorkspace} from './extensions.js';
import {retirePageFeatures, setNativeNavigator} from '../native-lifecycle.js';

const admittedGeneration = 'session-a';
const config = window.config;

function rootMarkup(mode = 'page') {
  return `<div data-extension-${mode} data-extension-contract-version="1" data-app-sub-url="" data-extension-id="example" data-extension-${mode}-id="home" data-extension-entry="/-/native/assets/example/${mode}.js" data-extension-asset-base="/-/native/assets/example/" data-extension-api-base="/-/native/${mode}s/example/home/api/" data-extension-session-generation="${admittedGeneration}"></div>`;
}

function installDocument({panel = false} = {}) {
  document.body.innerHTML = `<div class="page-content extensions">${rootMarkup()}<a href="/workspace#section">fragment</a><h2 id="section">Section</h2></div><aside data-extension-panels data-app-sub-url="" data-extension-session-generation="${admittedGeneration}">${panel ? rootMarkup('panel') : ''}</aside>`;
  document.body.dataset.pageData = '{}';
  window.config = {...config, appSubUrl: '', pageData: {}};
  return {
    root: document.querySelector('[data-extension-page]'),
    panel: document.querySelector('[data-extension-panel]'),
    home: document.querySelector('[data-extension-panels]'),
    content: document.querySelector('.page-content'),
  };
}

function validSession() {
  return {status: 204, headers: new Headers({'X-Extension-Session-Generation': admittedGeneration})};
}

function navigateEvent(route) {
  window.dispatchEvent(new CustomEvent('extension-workspace-navigation', {detail: {route, params: {}}}));
}

async function settle() {
  await new Promise((resolve) => setTimeout(resolve, 0));
}

beforeEach(() => {
  window.history.replaceState(null, '', '/workspace');
});

afterEach(() => {
  window.dispatchEvent(new Event('pagehide'));
  retirePageFeatures();
  setNativeNavigator(null);
  window.config = config;
  document.body.innerHTML = '';
  delete document.body.dataset.pageData;
  vi.restoreAllMocks();
});

test.each(['pages-first', 'workspace-first'])('both initialization adapters are idempotent and share one session observer (%s)', async (order) => {
  installDocument();
  const mount = vi.fn(() => ({dispose: vi.fn()}));
  const importer = vi.fn(async () => ({mount}));
  const pagesFetcher = vi.fn(async () => validSession());
  const workspaceFetcher = vi.fn(async () => validSession());
  const pages = () => initExtensionPages(importer, pagesFetcher);
  const workspace = () => initExtensionWorkspace(workspaceFetcher);
  if (order === 'pages-first') {
    pages();
    pages();
    workspace();
    workspace();
  } else {
    workspace();
    workspace();
    pages();
    pages();
  }
  await vi.waitFor(() => expect(mount).toHaveBeenCalledTimes(1));
  window.dispatchEvent(new Event('focus'));
  await settle();
  expect(workspaceFetcher).toHaveBeenCalledTimes(1);
  expect(pagesFetcher).not.toHaveBeenCalled();
  expect(importer).toHaveBeenCalledTimes(1);
});

test.each(['missing-generation', 'invalid-prefix'])('workspace initialization also retires native state after pages-first invalidation (%s)', (invalidAdmission) => {
  const {home, content} = installDocument();
  content.textContent = 'Private native content';
  window.config.pageData = {private: true};
  if (invalidAdmission === 'missing-generation') delete home.dataset.extensionSessionGeneration;
  else home.dataset.appSubUrl = '/malformed/';
  const importer = vi.fn();
  const fetcher = vi.fn();
  initExtensionPages(importer, fetcher);
  initExtensionWorkspace(fetcher);
  expect(content.textContent).toContain('Your session changed');
  expect(content.textContent).not.toContain('Private native content');
  expect(window.config.pageData).toEqual({});
  expect(home.hasAttribute('hidden')).toBe(true);
  expect(home.hasAttribute('inert')).toBe(true);
  expect(importer).not.toHaveBeenCalled();
  expect(fetcher).not.toHaveBeenCalled();
});

test('a fragment transition preserves the exact pending async mount and its transport', async () => {
  const {root} = installDocument();
  const completion = Promise.withResolvers();
  let context;
  const mount = vi.fn((_root, admittedContext) => {
    context = admittedContext;
    return completion.promise;
  });
  const importer = vi.fn(async () => ({mount}));
  const fetcher = vi.fn(async () => new Response('ok'));
  initExtensionPages(importer, fetcher);
  initExtensionWorkspace(fetcher);
  await vi.waitFor(() => expect(mount).toHaveBeenCalledTimes(1));
  document.querySelector('a').click();
  expect(window.location.hash).toBe('#section');
  const dispose = vi.fn();
  const retire = vi.fn();
  completion.resolve({dispose, retire});
  await settle();
  expect(root.isConnected).toBe(true);
  expect(document.querySelector('[data-extension-page]')).toBe(root);
  expect(importer).toHaveBeenCalledTimes(1);
  expect(dispose).not.toHaveBeenCalled();
  expect(retire).not.toHaveBeenCalled();
  await expect(context.request('read')).resolves.toBeInstanceOf(Response);
});

test('successful session validation cannot release a sheltered page until its exact root is readopted', async () => {
  const {root, home, content} = installDocument();
  const suspend = vi.fn();
  const resume = vi.fn();
  const dispose = vi.fn();
  const check = Promise.withResolvers();
  const fetcher = vi.fn(() => check.promise);
  initExtensionPages(async () => ({mount: () => ({suspend, resume, dispose})}), fetcher);
  await settle();
  window.dispatchEvent(new CustomEvent('extension-workspace-will-navigate'));
  content.replaceChildren(document.createTextNode('Native content'));
  home.setAttribute('hidden', '');
  navigateEvent('/team/repo');
  expect(home.querySelector('[data-extension-page-park]').contains(root)).toBe(true);
  window.dispatchEvent(new Event('focus'));
  await vi.waitFor(() => expect(fetcher).toHaveBeenCalledTimes(1));
  check.resolve(validSession());
  await settle();
  expect(suspend).toHaveBeenCalledTimes(1);
  expect(resume).not.toHaveBeenCalled();
  expect(dispose).not.toHaveBeenCalled();

  content.innerHTML = rootMarkup();
  home.removeAttribute('hidden');
  navigateEvent('/workspace');
  expect(content.querySelector('[data-extension-page]')).toBe(root);
  expect(resume).toHaveBeenCalledExactlyOnceWith({sessionGeneration: admittedGeneration});
});

test('a late owner inherits document and session holds and cannot resume from a hidden-document focus check', async () => {
  installDocument();
  const mountResult = Promise.withResolvers();
  const firstCheck = Promise.withResolvers();
  const secondCheck = Promise.withResolvers();
  const fetcher = vi.fn().mockReturnValueOnce(firstCheck.promise).mockReturnValueOnce(secondCheck.promise);
  const mount = vi.fn(() => mountResult.promise);
  const suspend = vi.fn();
  const resume = vi.fn();
  const visibility = vi.spyOn(document, 'visibilityState', 'get').mockReturnValue('visible');
  initExtensionPages(async () => ({mount}), fetcher);
  await vi.waitFor(() => expect(mount).toHaveBeenCalledTimes(1));
  visibility.mockReturnValue('hidden');
  document.dispatchEvent(new Event('visibilitychange'));
  window.dispatchEvent(new Event('focus'));
  await vi.waitFor(() => expect(fetcher).toHaveBeenCalledTimes(1));
  mountResult.resolve({suspend, resume, dispose: vi.fn()});
  await settle();
  expect(suspend).toHaveBeenCalledTimes(1);
  firstCheck.resolve(validSession());
  await settle();
  expect(resume).not.toHaveBeenCalled();

  visibility.mockReturnValue('visible');
  document.dispatchEvent(new Event('visibilitychange'));
  await vi.waitFor(() => expect(fetcher).toHaveBeenCalledTimes(2));
  expect(resume).not.toHaveBeenCalled();
  secondCheck.resolve(validSession());
  await settle();
  expect(resume).toHaveBeenCalledExactlyOnceWith({sessionGeneration: admittedGeneration});
});

test('throwing contribution callbacks cannot stop peer updates, live checks, resumption or terminal cleanup', async () => {
  const {root, panel, home} = installDocument({panel: true});
  root.textContent = 'Private page';
  panel.textContent = 'Private panel';
  const report = vi.spyOn(console, 'error').mockImplementation(() => {});
  const check = Promise.withResolvers();
  const fetcher = vi.fn(() => check.promise);
  const handles = [];
  const contexts = [];
  const importer = async () => ({mount: (root, context) => {
    const fail = root.hasAttribute('data-extension-page');
    const callback = () => vi.fn(() => { if (fail) throw new Error('Contribution callback failed'); });
    const handle = {update: callback(), suspend: callback(), resume: callback(), retire: callback(), dispose: callback()};
    handles.push(handle);
    contexts.push(context);
    return handle;
  }});
  initExtensionPages(importer, fetcher);
  initExtensionWorkspace(fetcher);
  await vi.waitFor(() => expect(handles).toHaveLength(2));
  navigateEvent('/workspace#next');
  expect(handles[0].update).toHaveBeenCalled();
  expect(handles[1].update).toHaveBeenCalled();
  window.dispatchEvent(new Event('focus'));
  await vi.waitFor(() => expect(fetcher).toHaveBeenCalledTimes(1));
  for (const handle of handles) expect(handle.suspend).toHaveBeenCalledTimes(1);
  check.resolve(validSession());
  await settle();
  for (const handle of handles) expect(handle.resume).toHaveBeenCalledExactlyOnceWith({sessionGeneration: admittedGeneration});

  window.dispatchEvent(new Event('extension-workspace-invalidated'));
  for (const handle of handles) {
    expect(handle.retire).toHaveBeenCalledTimes(1);
    expect(handle.dispose).toHaveBeenCalledTimes(1);
  }
  for (const context of contexts) expect(() => context.request('read')).toThrow('retired');
  expect(home.hasAttribute('hidden')).toBe(true);
  expect(home.hasAttribute('inert')).toBe(true);
  expect(document.querySelector('.page-content').textContent).toContain('Your session changed');
  expect(report).toHaveBeenCalled();
});

test('removing one pending page cancels its exact owner without canceling a connected panel mount', async () => {
  const {root, panel} = installDocument({panel: true});
  const pageResult = Promise.withResolvers();
  const panelResult = Promise.withResolvers();
  const mount = vi.fn((element) => element === root ? pageResult.promise : panelResult.promise);
  const importer = vi.fn(async () => ({mount}));
  initExtensionPages(importer, vi.fn());
  await vi.waitFor(() => expect(mount).toHaveBeenCalledTimes(2));
  root.remove();
  navigateEvent('/team/repo');
  const pageHandle = {retire: vi.fn(), dispose: vi.fn()};
  const panelHandle = {retire: vi.fn(), dispose: vi.fn()};
  pageResult.resolve(pageHandle);
  panelResult.resolve(panelHandle);
  await settle();
  expect(pageHandle.retire).toHaveBeenCalledTimes(1);
  expect(pageHandle.dispose).toHaveBeenCalledTimes(1);
  expect(panel.isConnected).toBe(true);
  expect(panelHandle.retire).not.toHaveBeenCalled();
  expect(panelHandle.dispose).not.toHaveBeenCalled();
  expect(importer).toHaveBeenCalledTimes(2);
});
