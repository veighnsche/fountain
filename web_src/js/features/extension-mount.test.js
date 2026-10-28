import {vi} from 'vitest';
import {mountContribution, parseMountDataset, createScopedRequest, createScopedStream} from './extension-mount.js';
import {mount as mountExamplePage} from '../../../contrib/extensions/example/assets/page.js';
import {mount as mountExamplePanel} from '../../../contrib/extensions/example/assets/panel.js';
import {mount as mountExampleWorkspace} from '../../../contrib/extensions/example/assets/workspace.js';

function pageRoot() {
  const root = document.createElement('div');
  root.dataset.appSubUrl = '';
  root.dataset.extensionRepositoryId = '7';
  root.dataset.extensionEntry = '/-/native/assets/example/main.js';
  root.dataset.extensionAssetBase = '/-/native/assets/example/';
  root.dataset.extensionApiBase = '/-/native/pages/example/home/api/';
  root.dataset.extensionId = 'example';
  root.dataset.extensionPageId = 'home';
  root.dataset.extensionSessionGeneration = 'session-a';
  root.dataset.extensionContractVersion = '1';
  return root;
}

class FakeSocket {
  constructor(url) {
    this.url = url;
    this.closed = false;
  }
  close() {
    this.closed = true;
  }
}

const flush = () => new Promise((resolve) => setTimeout(resolve, 0));

test.each([['page', mountExamplePage], ['workspace', mountExampleWorkspace]])('example %s refuses a different consumer contract version before effects', async (_name, mount) => {
  const root = document.createElement('div');
  root.textContent = 'unchanged';
  const request = vi.fn();
  const stream = vi.fn();
  const createElement = vi.spyOn(document, 'createElement');
  try {
    await expect(mount(root, {contractVersion: 2, request, stream})).rejects.toThrow('Unsupported browser contract version');
    expect(root.textContent).toBe('unchanged');
    expect(createElement).not.toHaveBeenCalled();
    expect(request).not.toHaveBeenCalled();
    expect(stream).not.toHaveBeenCalled();
  } finally {
    createElement.mockRestore();
  }
});

test('example panel refuses a different consumer contract version before effects', () => {
  const root = document.createElement('div');
  const request = vi.fn();
  const stream = vi.fn();
  const createElement = vi.spyOn(document, 'createElement');
  const setInterval = vi.spyOn(window, 'setInterval');
  try {
    expect(() => mountExamplePanel(root, {contractVersion: 2, request, stream})).toThrow('Unsupported browser contract version');
    expect(root.childNodes).toHaveLength(0);
    expect(root.className).toBe('');
    expect(createElement).not.toHaveBeenCalled();
    expect(setInterval).not.toHaveBeenCalled();
    expect(request).not.toHaveBeenCalled();
    expect(stream).not.toHaveBeenCalled();
  } finally {
    setInterval.mockRestore();
    createElement.mockRestore();
  }
});

test.each([['panel', mountExamplePanel], ['shared workspace', mountExampleWorkspace]])('example %s lifecycle pauses visible work and retains its connected root and stream until retirement', async (_name, mount) => {
  vi.useFakeTimers();
  const sockets = [];
  class ExampleSocket extends EventTarget {
    readyState = WebSocket.OPEN;
    send = vi.fn();
    constructor() {
      super();
      sockets.push(this);
    }
    close() {
      this.readyState = WebSocket.CLOSED;
      this.dispatchEvent(new Event('close'));
    }
  }
  const root = pageRoot();
  document.body.append(root);
  let owner;
  try {
    const fetcher = vi.fn().mockResolvedValue({ok: true, text: async () => 'private notes', json: async () => ({actor: {id: '7'}})});
    owner = await mountContribution(root, {importer: async () => ({mount}), fetcher, websocketClass: ExampleSocket});
    await vi.advanceTimersByTimeAsync(0);
    const input = root.querySelector('textarea');
    const lifetime = root.querySelector('[data-panel-lifetime]');
    const streamStatus = root.querySelector('[data-stream-connection]');
    const socket = sockets[0];
    socket.dispatchEvent(new MessageEvent('message', {data: JSON.stringify({hello: 'retained-stream'})}));
    expect(input.value).toBe('private notes');
    expect(vi.getTimerCount()).toBe(1);
    owner.update({mode: 'page', route: '/workspace', params: {}, visible: true});
    expect(lifetime.title).toBe('page: /workspace');
    await vi.advanceTimersByTimeAsync(1000);
    const beforeSuspend = lifetime.textContent;
    owner.suspend();
    expect(root.hidden).toBe(true);
    expect(vi.getTimerCount()).toBe(0);
    await vi.advanceTimersByTimeAsync(3000);
    expect(lifetime.textContent).toBe(beforeSuspend);
    expect(socket.readyState).toBe(WebSocket.OPEN);
    expect(input.value).toBe('private notes');
    owner.resume({sessionGeneration: 'session-a'});
    expect(root.hidden).toBe(false);
    expect(vi.getTimerCount()).toBe(1);
    expect(lifetime.textContent).not.toBe(beforeSuspend);
    owner.update({mode: 'panel', route: '/admin', params: {}, visible: false});
    expect(root.hidden).toBe(true);
    expect(vi.getTimerCount()).toBe(0);
    owner.update({mode: 'panel', route: '/admin/users', params: {}, visible: true});
    expect(vi.getTimerCount()).toBe(1);
    expect(owner.root).toBe(root);
    expect(root.isConnected).toBe(true);
    expect(root.querySelector('textarea')).toBe(input);
    expect(sockets).toHaveLength(1);
    expect(socket.readyState).toBe(WebSocket.OPEN);
    owner.retire('session-changed');
    expect(socket.readyState).toBe(WebSocket.CLOSED);
    expect(vi.getTimerCount()).toBe(0);
    expect(fetcher.mock.calls[0][1].signal.aborted).toBe(true);
    expect(input.value).toBe('');
    expect(root.hidden).toBe(true);
    socket.dispatchEvent(new MessageEvent('message', {data: JSON.stringify({hello: 'late'})}));
    expect(streamStatus.dataset.streamConnection).toBeUndefined();
  } finally {
    owner?.dispose();
    root.remove();
    vi.useRealTimers();
  }
});

test('mounts a retained owner with scoped request and declared stream access', async () => {
  const root = pageRoot();
  const dispose = vi.fn();
  const mount = vi.fn(() => dispose);
  const importer = vi.fn(async () => ({mount}));
  const fetcher = vi.fn().mockResolvedValue({status: 200});
  const owner = await mountContribution(root, {importer, fetcher, websocketClass: FakeSocket});
  expect(owner).not.toBeNull();
  expect(owner.root).toBe(root);
  expect(importer).toHaveBeenCalledWith(`${window.location.origin}/-/native/assets/example/main.js`);
  expect(mount).toHaveBeenCalledWith(root, expect.objectContaining({
    extensionId: 'example', pageId: 'home', panelId: undefined, appSubUrl: '',
    repositoryId: '7', sessionGeneration: 'session-a', contractVersion: 1,
    request: expect.any(Function), stream: expect.any(Function),
  }));
  await owner.context.request('context');
  expect(fetcher).toHaveBeenCalledWith(`${window.location.origin}/-/native/pages/example/home/api/context`, expect.objectContaining({
    credentials: 'same-origin', redirect: 'error', cache: 'no-store',
  }));
  expect(fetcher.mock.calls[0][1].headers.get('X-Extension-Session-Generation')).toBe('session-a');
  // Declared consumer access: ContributionStream {socket, close}.
  const contribution = owner.context.stream('socket');
  expect(contribution.socket).toBeInstanceOf(FakeSocket);
  expect(contribution.socket.url).toBe(`${window.location.origin}/-/native/pages/example/home/api/socket`.replace(/^http/, 'ws'));
  contribution.close();
  expect(contribution.socket.closed).toBe(true);
  owner.dispose();
  expect(dispose).toHaveBeenCalledOnce();
});

test('mounts a panel under an installation subpath', async () => {
  const root = document.createElement('div');
  root.dataset.appSubUrl = '/forge';
  root.dataset.extensionEntry = '/forge/-/native/assets/example/panel.js';
  root.dataset.extensionAssetBase = '/forge/-/native/assets/example/';
  root.dataset.extensionApiBase = '/forge/-/native/panels/example/notes/api/';
  root.dataset.extensionId = 'example';
  root.dataset.extensionPanelId = 'notes';
  root.dataset.extensionSessionGeneration = 'session-a';
  root.dataset.extensionContractVersion = '1';
  const mount = vi.fn();
  const owner = await mountContribution(root, {importer: vi.fn(async () => ({mount})), websocketClass: FakeSocket});
  expect(owner).not.toBeNull();
  expect(mount).toHaveBeenCalledWith(root, expect.objectContaining({
    pageId: undefined, panelId: 'notes', repositoryId: undefined, contractVersion: 1,
  }));
  owner.dispose();
});

test('mounts a linked owner panel on the canonical page API identity', async () => {
  const root = document.createElement('div');
  root.dataset.appSubUrl = '';
  root.dataset.extensionEntry = '/-/native/assets/example/main.js';
  root.dataset.extensionAssetBase = '/-/native/assets/example/';
  root.dataset.extensionApiBase = '/-/native/pages/example/spaces/api/';
  root.dataset.extensionId = 'example';
  root.dataset.extensionPanelId = 'workspace';
  root.dataset.extensionOwnerId = 'workspace';
  root.dataset.extensionSessionGeneration = 'session-a';
  root.dataset.extensionContractVersion = '1';
  const mount = vi.fn();
  const owner = await mountContribution(root, {importer: vi.fn(async () => ({mount})), websocketClass: FakeSocket});
  expect(owner).not.toBeNull();
  expect(mount).toHaveBeenCalledWith(root, expect.objectContaining({
    pageId: undefined, panelId: 'workspace', ownerId: 'workspace',
    apiBase: `${window.location.origin}/-/native/pages/example/spaces/api/`,
  }));
  owner.dispose();
  root.dataset.extensionOwnerId = 'Bad Owner';
  expect(parseMountDataset(root).ok).toBe(false);
});

test('programmatic navigation delegates to the host and retires with the owner', async () => {
  const root = pageRoot();
  const navigator = vi.fn();
  const owner = await mountContribution(root, {importer: vi.fn(async () => ({mount: vi.fn()})), websocketClass: FakeSocket, navigator});
  owner.context.navigate('/owner/repo');
  expect(navigator).toHaveBeenLastCalledWith('/owner/repo', {replace: false});
  owner.context.navigate('/workspace/requests/d0123456789abcdef01234567', {replace: true});
  expect(navigator).toHaveBeenLastCalledWith('/workspace/requests/d0123456789abcdef01234567', {replace: true});
  owner.context.navigate('/workspace/new', {});
  expect(navigator).toHaveBeenLastCalledWith('/workspace/new', {replace: false});
  expect(() => owner.context.navigate('/owner/repo', {replace: 'yes'})).toThrow('Navigation history option is invalid');
  expect(navigator).toHaveBeenCalledTimes(3);
  expect(() => owner.context.navigate('')).toThrow('Navigation path is invalid');
  owner.retire('authority-revoked');
  expect(() => owner.context.navigate('/owner/repo')).toThrow('Contribution transport is retired');
});

test('drives ordered update, suspend, resume, and retire with exact identities', async () => {
  const root = pageRoot();
  const calls = [];
  const handle = {
    update: vi.fn((presentation) => calls.push(['update', presentation.route])),
    suspend: vi.fn(() => calls.push(['suspend'])),
    resume: vi.fn(() => calls.push(['resume'])),
    retire: vi.fn(() => calls.push(['retire'])),
    dispose: vi.fn(() => calls.push(['dispose'])),
  };
  const owner = await mountContribution(root, {importer: vi.fn(async () => ({mount: () => handle})), websocketClass: FakeSocket});
  const contribution = owner.context.stream('socket');
  const socket = contribution.socket;
  owner.update({mode: 'page', route: '/workspace', params: {}, visible: true});
  owner.update({mode: 'page', route: '/team/repo', params: {}, repositoryId: '7', visible: true});
  expect(handle.update).toHaveBeenCalledTimes(2);
  expect(socket.closed).toBe(false);
  owner.suspend();
  expect(socket.closed).toBe(false);
  expect(handle.suspend).toHaveBeenCalledOnce();
  owner.resume({sessionGeneration: 'session-a'});
  expect(handle.resume).toHaveBeenCalledWith({sessionGeneration: 'session-a'});
  expect(socket.closed).toBe(false);
  expect(owner.root).toBe(root);
  expect(owner.context.stream).toBeTypeOf('function');
  owner.retire('session-changed');
  expect(socket.closed).toBe(true);
  expect(handle.retire).toHaveBeenCalledWith('session-changed');
  expect(calls).toEqual([
    ['update', '/workspace'], ['update', '/team/repo'],
    ['suspend'], ['resume'], ['retire'], ['dispose'],
  ]);
  owner.update({mode: 'page', route: '/late', params: {}, visible: true});
  expect(handle.update).toHaveBeenCalledTimes(2);
});

test('refuses invalid mounts without importing or fetching', async () => {
  const cases = [
    (root) => { root.dataset.extensionEntry = '/-/native/assets/other/main.js' },
    (root) => { root.dataset.extensionApiBase = '/-/native/pages/example/other/api/' },
    (root) => { root.dataset.extensionApiBase = '/-/native/pages/other/home/api/' },
    (root) => { delete root.dataset.extensionSessionGeneration },
    (root) => { delete root.dataset.extensionContractVersion },
    (root) => { root.dataset.extensionContractVersion = '2' },
    (root) => { root.dataset.appSubUrl = '/wrong' },
  ];
  for (const mutate of cases) {
    const root = pageRoot();
    mutate(root);
    expect(parseMountDataset(root).ok).toBe(false);
    const importer = vi.fn(async () => ({mount: vi.fn()}));
    const fetcher = vi.fn();
    const owner = await mountContribution(root, {importer, fetcher, websocketClass: FakeSocket});
    expect(owner).toBeNull();
    expect(root.textContent).toBe('This view is unavailable.');
    expect(importer).not.toHaveBeenCalled();
    expect(fetcher).not.toHaveBeenCalled();
  }
});

test('rejects out-of-scope request and stream targets', async () => {
  const root = pageRoot();
  const owner = await mountContribution(root, {importer: vi.fn(async () => ({mount: vi.fn()})), websocketClass: FakeSocket});
  expect(() => owner.context.request('../other')).toThrow('Product API URL is invalid');
  expect(() => owner.context.request('https://elsewhere.example/secret')).toThrow('Product API URL is invalid');
  expect(() => owner.context.request('/team%2frepo')).toThrow('Product API URL is invalid');
  expect(() => owner.context.stream('../other')).toThrow('Product stream URL is invalid');
  expect(() => owner.context.stream('socket?token=abc')).toThrow('Product stream URL is invalid');
  expect(() => owner.context.stream('https://elsewhere.example/socket')).toThrow('Product stream URL is invalid');
  owner.dispose();
});

test('revokes transport on retire and disposes exactly once', async () => {
  const root = pageRoot();
  const dispose = vi.fn();
  const owner = await mountContribution(root, {importer: vi.fn(async () => ({mount: () => dispose})), websocketClass: FakeSocket});
  owner.retire('authority-revoked');
  expect(dispose).toHaveBeenCalledOnce();
  expect(() => owner.context.request('context')).toThrow('Contribution transport is retired');
  expect(() => owner.context.stream('socket')).toThrow('Contribution transport is retired');
  owner.retire('authority-revoked');
  owner.dispose();
  expect(dispose).toHaveBeenCalledOnce();
});

test('closes streams and revokes helpers when mount throws synchronously', async () => {
  const root = pageRoot();
  let captured;
  let opened;
  const mount = vi.fn((_ignored, context) => {
    captured = context;
    opened = context.stream('socket');
    throw new Error('mount exploded');
  });
  const silence = vi.spyOn(console, 'error').mockImplementation(() => {});
  try {
    const owner = await mountContribution(root, {importer: vi.fn(async () => ({mount})), websocketClass: FakeSocket});
    expect(owner).toBeNull();
    expect(root.textContent).toBe('Could not load this view.');
    expect(opened.socket.closed).toBe(true);
    expect(() => captured.request('context')).toThrow('Contribution transport is retired');
    expect(() => captured.stream('socket')).toThrow('Contribution transport is retired');
  } finally {
    silence.mockRestore();
  }
});

test('closes streams and revokes helpers when mount rejects', async () => {
  const root = pageRoot();
  let captured;
  let opened;
  const mount = vi.fn(async (_ignored, context) => {
    captured = context;
    opened = context.stream('socket');
    throw new Error('async mount failed');
  });
  const silence = vi.spyOn(console, 'error').mockImplementation(() => {});
  try {
    const owner = await mountContribution(root, {importer: vi.fn(async () => ({mount})), websocketClass: FakeSocket});
    expect(owner).toBeNull();
    expect(opened.socket.closed).toBe(true);
    expect(() => captured.request('context')).toThrow('Contribution transport is retired');
    expect(() => captured.stream('later')).toThrow('Contribution transport is retired');
  } finally {
    silence.mockRestore();
  }
});

test('prevents mount after host invalidation while import is pending', async () => {
  const root = pageRoot();
  const controller = new AbortController();
  let resolveImport;
  const importer = vi.fn(() => new Promise((resolve) => { resolveImport = resolve }));
  const mount = vi.fn();
  const pending = mountContribution(root, {importer, websocketClass: FakeSocket, signal: controller.signal});
  controller.abort();
  resolveImport({mount});
  const owner = await pending;
  expect(owner).toBeNull();
  expect(mount).not.toHaveBeenCalled();
});

test('revokes pending helpers and streams when mount never settles', async () => {
  const root = pageRoot();
  const controller = new AbortController();
  let captured;
  let opened;
  let resolveMount;
  const retire = vi.fn();
  const dispose = vi.fn();
  const mount = vi.fn((_ignored, context) => {
    captured = context;
    opened = context.stream('socket');
    return new Promise((resolve) => { resolveMount = resolve });
  });
  const pending = mountContribution(root, {importer: vi.fn(async () => ({mount})), websocketClass: FakeSocket, signal: controller.signal});
  await flush();
  expect(mount).toHaveBeenCalledOnce();
  controller.abort();
  expect(opened.socket.closed).toBe(true);
  expect(() => captured.request('context')).toThrow('Contribution transport is retired');
  expect(() => captured.stream('socket')).toThrow('Contribution transport is retired');
  resolveMount({retire, dispose});
  const owner = await pending;
  expect(owner).toBeNull();
  expect(retire).toHaveBeenCalledWith('authority-revoked');
  expect(dispose).toHaveBeenCalledOnce();
});

test('retires a late-settling retire-only handle after invalidation', async () => {
  const root = pageRoot();
  const controller = new AbortController();
  let resolveMount;
  const retire = vi.fn();
  const mount = vi.fn(() => new Promise((resolve) => { resolveMount = resolve }));
  const pending = mountContribution(root, {importer: vi.fn(async () => ({mount})), websocketClass: FakeSocket, signal: controller.signal});
  await flush();
  expect(mount).toHaveBeenCalledOnce();
  controller.abort();
  resolveMount({retire});
  const owner = await pending;
  expect(owner).toBeNull();
  expect(retire).toHaveBeenCalledWith('authority-revoked');
});

test('retires the owner when the host aborts after settle', async () => {
  const root = pageRoot();
  const controller = new AbortController();
  const retire = vi.fn();
  const dispose = vi.fn();
  const owner = await mountContribution(root, {
    importer: vi.fn(async () => ({mount: () => ({retire, dispose})})),
    websocketClass: FakeSocket, signal: controller.signal,
  });
  expect(owner).not.toBeNull();
  controller.abort();
  expect(retire).toHaveBeenCalledWith('authority-revoked');
  expect(dispose).toHaveBeenCalledOnce();
  expect(() => owner.context.request('context')).toThrow('Contribution transport is retired');
});

test('reports unavailable streams without a websocket implementation', () => {
  expect(() => createScopedStream('https://forge.example/-/native/pages/example/home/api/', undefined)).toThrow('Contribution streams are unavailable');
  expect(() => createScopedRequest).not.toThrow();
});

function deferredFetcher() {
  let resolveFetch;
  const fetcher = vi.fn(() => new Promise((resolve) => { resolveFetch = resolve }));
  return {fetcher, resolveFetch: (value) => resolveFetch(value)};
}

test('cancels in-flight requests on retire and refuses the late response', async () => {
  const root = pageRoot();
  const {fetcher, resolveFetch} = deferredFetcher();
  const owner = await mountContribution(root, {importer: vi.fn(async () => ({mount: vi.fn()})), fetcher, websocketClass: FakeSocket});
  const pending = owner.context.request('context');
  owner.retire('authority-revoked');
  expect(fetcher.mock.calls[0][1].signal.aborted).toBe(true);
  resolveFetch({status: 200});
  await expect(pending).rejects.toThrow('Contribution transport is retired');
});

test('cancels in-flight requests on host invalidation', async () => {
  const root = pageRoot();
  const controller = new AbortController();
  const {fetcher, resolveFetch} = deferredFetcher();
  const owner = await mountContribution(root, {
    importer: vi.fn(async () => ({mount: vi.fn()})), fetcher, websocketClass: FakeSocket, signal: controller.signal,
  });
  const pending = owner.context.request('context');
  controller.abort();
  expect(fetcher.mock.calls[0][1].signal.aborted).toBe(true);
  resolveFetch({status: 200});
  await expect(pending).rejects.toThrow('Contribution transport is retired');
});

test('refuses late responses after mount failure', async () => {
  const root = pageRoot();
  const {fetcher, resolveFetch} = deferredFetcher();
  let pending;
  const mount = vi.fn((_ignored, context) => {
    pending = context.request('context');
    throw new Error('mount exploded');
  });
  const silence = vi.spyOn(console, 'error').mockImplementation(() => {});
  try {
    const owner = await mountContribution(root, {importer: vi.fn(async () => ({mount})), fetcher, websocketClass: FakeSocket});
    expect(owner).toBeNull();
    resolveFetch({status: 200});
    await expect(pending).rejects.toThrow('Contribution transport is retired');
  } finally {
    silence.mockRestore();
  }
});

test('preserves explicit consumer cancellation alongside the owner lifetime', async () => {
  const root = pageRoot();
  const consumer = new AbortController();
  const fetcher = vi.fn().mockResolvedValue({status: 200});
  const owner = await mountContribution(root, {importer: vi.fn(async () => ({mount: vi.fn()})), fetcher, websocketClass: FakeSocket});
  const pending = owner.context.request('context', {signal: consumer.signal});
  consumer.abort();
  expect(fetcher.mock.calls[0][1].signal.aborted).toBe(true);
  await expect(pending).resolves.toEqual({status: 200});
  owner.dispose();
});

test('retains in-flight requests across update, suspend, and resume', async () => {
  const root = pageRoot();
  const {fetcher, resolveFetch} = deferredFetcher();
  const handle = {update: vi.fn(), suspend: vi.fn(), resume: vi.fn()};
  const owner = await mountContribution(root, {importer: vi.fn(async () => ({mount: () => handle})), fetcher, websocketClass: FakeSocket});
  const pending = owner.context.request('context');
  owner.update({mode: 'page', route: '/team/repo', params: {}, visible: true});
  owner.suspend();
  owner.resume({sessionGeneration: 'session-a'});
  expect(fetcher.mock.calls[0][1].signal.aborted).toBe(false);
  resolveFetch({status: 200});
  await expect(pending).resolves.toEqual({status: 200});
  owner.dispose();
});

test('a mismatched resume retires instead of resuming stale private state', async () => {
  const root = pageRoot();
  const handle = {
    suspend: vi.fn(),
    resume: vi.fn(),
    retire: vi.fn(),
    dispose: vi.fn(),
  };
  const owner = await mountContribution(root, {importer: vi.fn(async () => ({mount: () => handle})), websocketClass: FakeSocket});
  const contribution = owner.context.stream('socket');
  owner.suspend();
  expect(handle.suspend).toHaveBeenCalledOnce();
  owner.resume({sessionGeneration: 'session-b'});
  expect(handle.resume).not.toHaveBeenCalled();
  expect(handle.retire).toHaveBeenCalledWith('session-changed');
  expect(handle.dispose).toHaveBeenCalledOnce();
  expect(contribution.socket.closed).toBe(true);
  expect(() => owner.context.request('context')).toThrow('Contribution transport is retired');
  expect(() => owner.context.stream('socket')).toThrow('Contribution transport is retired');
  // A later well-formed resume cannot resurrect the retired owner.
  owner.resume({sessionGeneration: 'session-a'});
  expect(handle.resume).not.toHaveBeenCalled();
});

test('a missing resume validation retires instead of resuming', async () => {
  const root = pageRoot();
  const handle = {suspend: vi.fn(), resume: vi.fn(), retire: vi.fn(), dispose: vi.fn()};
  const owner = await mountContribution(root, {importer: vi.fn(async () => ({mount: () => handle})), websocketClass: FakeSocket});
  owner.suspend();
  owner.resume();
  expect(handle.resume).not.toHaveBeenCalled();
  expect(handle.retire).toHaveBeenCalledWith('session-changed');
});

test('refuses pre-aborted caller requests without sending', async () => {
  const root = pageRoot();
  const consumer = new AbortController();
  consumer.abort();
  const fetcher = vi.fn().mockResolvedValue({status: 200});
  const owner = await mountContribution(root, {importer: vi.fn(async () => ({mount: vi.fn()})), fetcher, websocketClass: FakeSocket});
  await expect(owner.context.request('context', {method: 'PUT', body: 'stale', signal: consumer.signal})).rejects.toThrow('This operation was aborted');
  expect(fetcher).not.toHaveBeenCalled();
  owner.dispose();
});

function abortAwareFetcher() {
  return vi.fn((_url, init) => Promise.resolve({
    status: 200,
    json: () => new Promise((_resolve, reject) => {
      if (init.signal.aborted) reject(new DOMException('This operation was aborted', 'AbortError'));
      else init.signal.addEventListener('abort', () => reject(new DOMException('This operation was aborted', 'AbortError')), {once: true});
    }),
  }));
}

test('aborts delayed body reads on caller cancellation', async () => {
  const root = pageRoot();
  const consumer = new AbortController();
  const fetcher = abortAwareFetcher();
  const owner = await mountContribution(root, {importer: vi.fn(async () => ({mount: vi.fn()})), fetcher, websocketClass: FakeSocket});
  const response = await owner.context.request('context', {signal: consumer.signal});
  const reading = response.json();
  consumer.abort();
  await expect(reading).rejects.toThrow('This operation was aborted');
  owner.dispose();
});

test('aborts delayed body reads on owner retirement', async () => {
  const root = pageRoot();
  const fetcher = abortAwareFetcher();
  const owner = await mountContribution(root, {importer: vi.fn(async () => ({mount: vi.fn()})), fetcher, websocketClass: FakeSocket});
  const response = await owner.context.request('context');
  const reading = response.json();
  owner.retire('authority-revoked');
  await expect(reading).rejects.toThrow('This operation was aborted');
});

test('aborts delayed body reads on owner retirement with explicit caller signal', async () => {
  const root = pageRoot();
  const consumer = new AbortController();
  const fetcher = abortAwareFetcher();
  const owner = await mountContribution(root, {importer: vi.fn(async () => ({mount: vi.fn()})), fetcher, websocketClass: FakeSocket});
  const response = await owner.context.request('context', {signal: consumer.signal});
  const reading = response.json();
  owner.retire('authority-revoked');
  await expect(reading).rejects.toThrow('This operation was aborted');
});

test('reused caller controller completes repeated polls without aborting completed reads', async () => {
  const root = pageRoot();
  const consumer = new AbortController();
  const fetcher = vi.fn(async () => ({status: 200, json: async () => ({ok: true})}));
  const owner = await mountContribution(root, {importer: vi.fn(async () => ({mount: vi.fn()})), fetcher, websocketClass: FakeSocket});
  for (let i = 0; i < 3; i++) {
    const response = await owner.context.request('context', {signal: consumer.signal});
    await expect(response.json()).resolves.toEqual({ok: true});
    expect(fetcher.mock.calls[i][1].signal.aborted).toBe(false);
  }
  expect(consumer.signal.aborted).toBe(false);
  const pending = owner.context.request('context', {signal: consumer.signal});
  owner.retire('authority-revoked');
  await expect(pending).rejects.toThrow('Contribution transport is retired');
});
