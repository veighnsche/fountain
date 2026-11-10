import {vi} from 'vitest';
import {h, onMounted, onUnmounted} from 'vue';

const destroy = vi.fn();
const originalLanguage = document.documentElement.lang;
const renderContext = () => h('div', 'Issue context');
const renderHeatmap = () => h('div', 'Heatmap');
vi.mock('../modules/tippy.js', () => ({createTippy: vi.fn(() => ({destroy})), showTemporaryTooltip: vi.fn()}));

function deferred() {
  let resolve;
  const promise = new Promise((accept) => { resolve = accept });
  return {promise, resolve};
}

beforeEach(() => {
  vi.resetModules();
  destroy.mockClear();
  document.body.innerHTML = '';
  document.documentElement.lang = 'en';
});

afterEach(async () => {
  (await import('./contextpopup.js')).retireContextPopups();
  (await import('./heatmap.js')).retireHeatmap();
  (await import('../markup/anchors.js')).retireMarkupAnchors();
  (await import('../markup/external.js')).retireExternal();
  document.body.innerHTML = '';
  document.documentElement.lang = originalLanguage;
  vi.doUnmock('../components/ContextPopup.vue');
  vi.doUnmock('../components/ActivityHeatmap.vue');
});

test('native issue popups own and retire their Vue app, tooltip and detached content', async () => {
  const events = [];
  vi.doMock('../components/ContextPopup.vue', () => ({default: {
    setup() {
      onMounted(() => events.push('mount'));
      onUnmounted(() => events.push('retire'));
      return renderContext;
    },
  }}));
  const {attachRefIssueContextPopup, retireContextPopups} = await import('./contextpopup.js');
  document.body.innerHTML = '<div class="page-content"><a class="ref-issue" href="/user2/repo1/issues/1">Issue</a></div>';
  const refs = document.querySelectorAll('.ref-issue');
  await attachRefIssueContextPopup(refs);
  await attachRefIssueContextPopup(refs);
  expect(events).toEqual(['mount']);
  expect(document.querySelector('.page-content').textContent).toBe('IssueIssue context');
  retireContextPopups();
  retireContextPopups();
  expect(events).toEqual(['mount', 'retire']);
  expect(destroy).toHaveBeenCalledOnce();
  expect(document.querySelector('.page-content').textContent).toBe('Issue');
});

test('a retired issue-popup import cannot create an app or body tooltip later', async () => {
  const loaded = deferred();
  const importer = vi.fn(() => loaded.promise);
  vi.doMock('../components/ContextPopup.vue', importer);
  const {attachRefIssueContextPopup, retireContextPopups} = await import('./contextpopup.js');
  document.body.innerHTML = '<div class="page-content"><a class="ref-issue" href="/user2/repo1/issues/1">Issue</a></div>';
  const pending = attachRefIssueContextPopup(document.querySelectorAll('.ref-issue'));
  await vi.waitFor(() => expect(importer).toHaveBeenCalledOnce());
  retireContextPopups();
  loaded.resolve({default: {render: () => h('div', 'Late private context')}});
  await pending;
  expect(document.body.textContent).toBe('Issue');
  expect(destroy).not.toHaveBeenCalled();
});

test('heatmap re-entry unmounts the old Vue app and excludes contribution-owned content', async () => {
  const events = [];
  vi.doMock('../components/ActivityHeatmap.vue', () => ({default: {
    setup() {
      onMounted(() => events.push('mount'));
      onUnmounted(() => events.push('retire'));
      return renderHeatmap;
    },
  }}));
  const {initHeatmap, retireHeatmap} = await import('./heatmap.js');
  document.body.innerHTML = '<div class="page-content"><div id="user-heatmap" data-heatmap-data="[]"></div></div>';
  await initHeatmap();
  await initHeatmap();
  retireHeatmap();
  expect(events).toEqual(['mount', 'retire']);
  document.body.innerHTML = '<div data-extension-panel><div id="user-heatmap" data-heatmap-data="[]">Product content</div></div>';
  await initHeatmap();
  expect(document.body.textContent).toBe('Product content');
  expect(events).toEqual(['mount', 'retire']);
});

test('native anchor re-entry binds once and leaves contribution markup untouched', async () => {
  const {initMarkupAnchors, retireMarkupAnchors} = await import('../markup/anchors.js');
  document.body.innerHTML = '<div class="markup"><h1 id="user-content-note">Note</h1><a name="note"></a></div><div data-extension-panel><div class="markup"><h1 id="product">Product</h1></div></div>';
  initMarkupAnchors();
  retireMarkupAnchors();
  initMarkupAnchors();
  expect(document.querySelectorAll('#user-content-note > .anchor')).toHaveLength(1);
  expect(document.querySelector('a[name="user-content-note"]')).not.toBeNull();
  expect(document.querySelector('#product > .anchor')).toBeNull();
});

test('external renderer retirement removes its pending window-message handler', async () => {
  const {renderExternal, retireExternal} = await import('../markup/external.js');
  document.body.innerHTML = '<iframe class="external-render"></iframe>';
  const frame = document.querySelector('iframe');
  // happy-dom 21 no longer loads src-less iframe pages; supply the frame's window.
  Object.defineProperty(frame, 'contentWindow', {value: {postMessage: vi.fn()}});
  renderExternal();
  expect(frame.contentWindow.postMessage).toHaveBeenCalledWith({requestOffsetHeight: true}, '*');
  retireExternal();
  window.dispatchEvent(new MessageEvent('message', {source: frame.contentWindow, data: {frameHeight: 400}}));
  expect(frame.height).toBe('');
});
