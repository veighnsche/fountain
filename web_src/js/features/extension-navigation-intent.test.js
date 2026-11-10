import {vi} from 'vitest';
import {navigateNative} from '../native-lifecycle.js';
import {initExtensionPages, initExtensionWorkspace} from './extensions.js';

const savedConfig = window.config;
const savedConfirm = window.confirm;
const nativePushState = window.history.pushState;
const nativeReplaceState = window.history.replaceState;
const indexKey = '__workspaceNavIndex';
let assign;
let replace;

function panelMarkup() {
  return '<div data-extension-panel data-extension-contract-version="1" data-app-sub-url="" data-extension-id="example" data-extension-panel-id="notes" data-extension-entry="/-/native/assets/example/panel.js" data-extension-asset-base="/-/native/assets/example/" data-extension-api-base="/-/native/panels/example/notes/api/" data-extension-session-generation="session-a"></div>';
}

function homeMarkup() {
  return `<aside data-extension-panels data-app-sub-url="" data-extension-session-generation="session-a" hidden inert>${panelMarkup()}</aside>`;
}

function enteredDocument(content = 'Entered native page') {
  return `<html><head><title>Entered</title></head><body data-page-data='{"entered":true}'><div class="full height"><main class="page-content">${content}</main></div>${homeMarkup()}</body></html>`;
}

function response(content) {
  return new Response(enteredDocument(content), {headers: {'X-Extension-Session-Generation': 'session-a'}});
}

function install(content = 'Original native page') {
  document.body.innerHTML = `<div class="full height"><main class="page-content">${content}</main></div>${homeMarkup()}`;
  document.body.dataset.pageData = '{"original":true}';
  window.config = {...savedConfig, pageData: {original: true}};
}

async function boot(fetcher, source = 'native') {
  initExtensionWorkspace(fetcher);
  if (source === 'native') return (path, options) => navigateNative(path, options);
  let context;
  initExtensionPages(async () => ({mount(_root, mountedContext) {
    context = mountedContext;
    return {dispose() {}};
  }}), fetcher);
  await vi.waitFor(() => expect(context).toBeDefined());
  return (path, options) => context.navigate(path, options);
}

function clickLink(path) {
  const link = document.createElement('a');
  link.href = path;
  document.body.append(link);
  link.click();
  link.remove();
}

async function settle() {
  for (let i = 0; i < 6; i++) await Promise.resolve();
}

function expectFullLoad(method, target) {
  const called = method === 'replace' ? replace : assign;
  const other = method === 'replace' ? assign : replace;
  expect(called).toHaveBeenCalledTimes(1);
  const url = new URL(called.mock.calls[0][0], window.location.origin);
  expect(url.pathname + url.search).toBe(target);
  expect(other).not.toHaveBeenCalled();
}

beforeEach(() => {
  window.history.pushState = nativePushState;
  window.history.replaceState = nativeReplaceState;
  nativeReplaceState.call(window.history, null, '', '/original');
  install();
  window.confirm = vi.fn(() => false);
  assign = vi.spyOn(window.location, 'assign').mockImplementation(() => {});
  replace = vi.spyOn(window.location, 'replace').mockImplementation(() => {});
});

afterEach(async () => {
  window.dispatchEvent(new Event('pagehide'));
  await settle();
  vi.useRealTimers();
  vi.restoreAllMocks();
  window.history.pushState = nativePushState;
  window.history.replaceState = nativeReplaceState;
  window.config = savedConfig;
  if (savedConfirm === undefined) delete window.confirm;
  else window.confirm = savedConfirm;
  document.body.replaceChildren();
});

test.each(['context', 'native'])('%s replace intent survives every full-document fallback', async (source) => {
  const fetcher = vi.fn();
  const navigate = await boot(fetcher, source);
  const failures = [
    ['top', '/user/login', null],
    ['http', '/http-failure', async () => new Response('Unavailable', {status: 503})],
    ['admission', '/partial-page', async () => new Response('<html><body>Partial</body></html>', {headers: {'X-Extension-Session-Generation': 'session-a'}})],
    ['fetch', '/fetch-failure', async () => { throw new Error('offline') }],
    ['body', '/body-failure', async () => ({ok: true, headers: new Headers({'X-Extension-Session-Generation': 'session-a'}), text: async () => { throw new Error('body lost') }})],
  ];
  for (const [_stage, target, fetch] of failures) {
    assign.mockClear();
    replace.mockClear();
    fetcher.mockImplementation(fetch ?? (() => { throw new Error('top destination must not be fetched') }));
    navigate(target, {replace: true});
    await vi.waitFor(() => expectFullLoad('replace', target));
    expect(document.querySelector('.page-content').textContent).toBe('Original native page');
    expect(window.config.pageData).toEqual({original: true});
    expect(window.location.pathname).toBe('/original');
  }
  expect(fetcher).toHaveBeenCalledTimes(4);
});

test.each(['context', 'native'])('%s current timeout preserves replace intent at the fifteen-second boundary', async (source) => {
  const fetcher = vi.fn((_url, {signal}) => new Promise((_resolve, reject) => {
    signal.addEventListener('abort', () => reject(new DOMException('Aborted', 'AbortError')), {once: true});
  }));
  const navigate = await boot(fetcher, source);
  vi.useFakeTimers();
  navigate('/slow-target', {replace: true});
  await vi.advanceTimersByTimeAsync(14999);
  expect(assign).not.toHaveBeenCalled();
  expect(replace).not.toHaveBeenCalled();
  await vi.advanceTimersByTimeAsync(1);
  expectFullLoad('replace', '/slow-target');
  expect(fetcher.mock.calls[0][1].signal.aborted).toBe(true);
});

test.each(['fetch', 'body'])('a superseded %s failure cannot trigger a document fallback', async (stage) => {
  const old = Promise.withResolvers();
  const text = vi.fn(() => old.promise);
  const fetcher = vi.fn().mockImplementationOnce(() => stage === 'fetch' ? old.promise : Promise.resolve({
    ok: true, headers: new Headers({'X-Extension-Session-Generation': 'session-a'}), text,
  })).mockResolvedValueOnce(response());
  const navigate = await boot(fetcher);
  navigate('/old-request', {replace: true});
  await settle();
  if (stage === 'body') expect(text).toHaveBeenCalledOnce();
  navigate('/current-request', {replace: true});
  await vi.waitFor(() => expect(document.querySelector('.page-content').textContent).toBe('Entered native page'));
  old.reject(new Error('retired request failed'));
  await settle();
  expect(fetcher.mock.calls[0][1].signal.aborted).toBe(true);
  expect(assign).not.toHaveBeenCalled();
  expect(replace).not.toHaveBeenCalled();
  expect(window.location.pathname).toBe('/current-request');
});

test('a superseded timeout cannot abort its newer navigation or fall back', async () => {
  const old = Promise.withResolvers();
  const current = Promise.withResolvers();
  const fetcher = vi.fn().mockReturnValueOnce(old.promise).mockReturnValueOnce(current.promise);
  const navigate = await boot(fetcher);
  vi.useFakeTimers();
  navigate('/old-slow-request', {replace: true});
  await vi.advanceTimersByTimeAsync(1000);
  navigate('/current-slow-request', {replace: true});
  await vi.advanceTimersByTimeAsync(14000);
  expect(fetcher.mock.calls[1][1].signal.aborted).toBe(false);
  expect(assign).not.toHaveBeenCalled();
  expect(replace).not.toHaveBeenCalled();
  old.reject(new Error('old timeout arrived late'));
  current.resolve(response());
  await settle();
  expect(window.location.pathname).toBe('/current-slow-request');
  expect(assign).not.toHaveBeenCalled();
  expect(replace).not.toHaveBeenCalled();
});

test('session authority loss aborts navigation and suppresses its late body error', async () => {
  const body = Promise.withResolvers();
  const text = vi.fn(() => body.promise);
  const fetcher = vi.fn((url) => Promise.resolve(url.endsWith('/-/native/session') ?
    {status: 204, headers: new Headers({'X-Extension-Session-Generation': 'session-b'})} :
    {ok: true, headers: new Headers({'X-Extension-Session-Generation': 'session-a'}), text}));
  const navigate = await boot(fetcher);
  navigate('/private-target', {replace: true});
  await vi.waitFor(() => expect(text).toHaveBeenCalledOnce());
  window.dispatchEvent(new Event('focus'));
  await vi.waitFor(() => expect(document.querySelector('.page-content').textContent).toContain('Your session changed'));
  body.reject(new Error('body failed after authority loss'));
  await settle();
  expect(fetcher.mock.calls[0][1].signal.aborted).toBe(true);
  expect(assign).not.toHaveBeenCalled();
  expect(replace).not.toHaveBeenCalled();
});

test('a failed initial history stamp leaves programmatic navigation on matching full loads', async () => {
  vi.spyOn(window.history, 'replaceState').mockImplementation(() => { throw new Error('stamp refused') });
  const fetcher = vi.fn().mockRejectedValue(new Error('shell must remain disabled'));
  const navigate = await boot(fetcher, 'context');
  navigate('/next-native-page', {replace: true});
  await vi.waitFor(() => expectFullLoad('replace', '/next-native-page'));
  expect(fetcher).not.toHaveBeenCalled();
  expect(document.querySelector('.page-content').textContent).toBe('Original native page');
  expect(window.config.pageData).toEqual({original: true});
});

test.each(['push', 'replace'])('a failed %s history write cannot publish entered native content or config', async (mode) => {
  let fail = false;
  const write = mode === 'push' ? nativePushState : nativeReplaceState;
  vi.spyOn(window.history, `${mode}State`).mockImplementation(function (...args) {
    if (fail) throw new Error('history write refused');
    return write.apply(this, args);
  });
  const navigate = await boot(vi.fn().mockResolvedValue(response()));
  fail = true;
  const length = window.history.length;
  navigate('/history-write-target', {replace: mode === 'replace'});
  await vi.waitFor(() => expectFullLoad(mode === 'push' ? 'assign' : 'replace', '/history-write-target'));
  expect(document.querySelector('.page-content').textContent).toBe('Original native page');
  expect(window.config.pageData).toEqual({original: true});
  expect(window.location.pathname).toBe('/original');
  expect(window.history.length).toBe(length);
});

test('failure after a committed push replaces that target instead of adding another entry', async () => {
  vi.spyOn(document.querySelector('.page-content'), 'replaceWith').mockImplementation(() => { throw new Error('swap failed') });
  const navigate = await boot(vi.fn().mockResolvedValue(response()));
  const length = window.history.length;
  navigate('/committed-target', {replace: false});
  await vi.waitFor(() => expectFullLoad('replace', '/committed-target'));
  expect(window.location.pathname).toBe('/committed-target');
  expect(window.history.length).toBe(length + 1);
});

test('traversal failure reloads its already-landed entry without adding or rewriting history', async () => {
  nativeReplaceState.call(window.history, {[indexKey]: 0, marker: 'landed'}, '', '/landed');
  nativePushState.call(window.history, {[indexKey]: 1, marker: 'tip'}, '', '/tip');
  await boot(vi.fn().mockRejectedValue(new Error('traversal fetch failed')));
  const push = vi.spyOn(window.history, 'pushState');
  const write = vi.spyOn(window.history, 'replaceState');
  const length = window.history.length;
  window.history.back();
  await vi.waitFor(() => expectFullLoad('replace', '/landed'));
  expect(window.history.length).toBe(length);
  expect(window.history.state).toEqual(expect.objectContaining({marker: 'landed', [indexKey]: 0}));
  expect(push).not.toHaveBeenCalled();
  expect(write).not.toHaveBeenCalled();
  window.dispatchEvent(new Event('pagehide'));
  window.history.forward();
  await vi.waitFor(() => expect(window.location.pathname).toBe('/tip'));
});

test.each(['/foreign-entry', '/displayed'])('an unindexed traversal to %s replaces the landed document without guessing history', async (landed) => {
  nativeReplaceState.call(window.history, {marker: 'foreign'}, '', landed);
  nativePushState.call(window.history, {[indexKey]: 4}, '', '/displayed');
  const fetcher = vi.fn();
  await boot(fetcher);
  const push = vi.spyOn(window.history, 'pushState');
  const go = vi.spyOn(window.history, 'go');
  const length = window.history.length;

  window.history.back();

  await vi.waitFor(() => expectFullLoad('replace', landed));
  expect(window.history.state).toEqual({marker: 'foreign'});
  expect(window.history.length).toBe(length);
  expect(fetcher).not.toHaveBeenCalled();
  expect(push).not.toHaveBeenCalled();
  expect(go).not.toHaveBeenCalled();
  expect(document.querySelector('.page-content').textContent).toBe('Original native page');
});

test.each(['compensation throws', 'index missing'])('refused traversal coherently leaves when %s', async (failure) => {
  nativeReplaceState.call(window.history, failure === 'index missing' ? {marker: 'foreign'} : {[indexKey]: 0}, '', '/landed');
  nativePushState.call(window.history, {[indexKey]: 1}, '', '/displayed');
  install('<form class="dirty"><textarea>Preserved draft</textarea></form>');
  vi.spyOn(window, 'confirm').mockReturnValue(false);
  const fetcher = vi.fn();
  await boot(fetcher);
  const go = vi.spyOn(window.history, 'go').mockImplementation(() => { throw new Error('compensation refused') });
  const push = vi.spyOn(window.history, 'pushState');
  const length = window.history.length;
  window.history.back();
  await vi.waitFor(() => expectFullLoad('replace', '/displayed'));
  expect(window.confirm).toHaveBeenCalledOnce();
  expect(go).toHaveBeenCalledTimes(failure === 'index missing' ? 0 : 1);
  expect(push).not.toHaveBeenCalled();
  expect(fetcher).not.toHaveBeenCalled();
  expect(window.history.length).toBe(length);
  expect(document.querySelector('textarea').value).toBe('Preserved draft');
});

test('same-query refresh and submitted-form dirty exemption belong only to GET submission', async () => {
  const forms = '<form id="search" class="dirty" method="get" action="/search"><input name="q" value="alice"></form>';
  install(forms);
  nativeReplaceState.call(window.history, null, '', '/search?q=alice');
  const fetcher = vi.fn().mockImplementation(async () => response(forms));
  const navigate = await boot(fetcher, 'context');
  const confirm = vi.spyOn(window, 'confirm').mockReturnValue(false);
  navigate('/search?q=alice', {replace: true});
  await settle();
  expect(fetcher).not.toHaveBeenCalled();
  expect(confirm).not.toHaveBeenCalled();
  document.querySelector('#search').requestSubmit();
  await vi.waitFor(() => expect(fetcher).toHaveBeenCalledTimes(1));
  await vi.waitFor(() => expect(window.config.pageData).toEqual({entered: true}));
  for (const request of [() => navigate('/elsewhere'), () => navigateNative('/elsewhere'), () => clickLink('/elsewhere')]) request();
  expect(confirm).toHaveBeenCalledTimes(3);
  expect(fetcher).toHaveBeenCalledTimes(1);
  document.querySelector('.page-content').insertAdjacentHTML('beforeend', '<form class="dirty"><textarea>Another draft</textarea></form>');
  document.querySelector('#search').requestSubmit();
  expect(confirm).toHaveBeenCalledTimes(4);
  expect(fetcher).toHaveBeenCalledTimes(1);
  expect(document.querySelector('textarea').value).toBe('Another draft');
});
