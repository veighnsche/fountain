import {GET, POST} from '../modules/fetch.js';
import {setPageConfig} from '../native-lifecycle.js';
import {initCompReactionSelector} from './comp/ReactionSelector.js';
import {initImageDiff, retireImageDiff} from './imagediff.js';
import {showErrorToast} from '../modules/toast.js';
import {initRepoDiffView, retireRepoDiffView, loadMoreFiles, initRepoDiffConversationNav, retireRepoDiffConversationNav} from './repo-diff.js';

vi.mock('./repo-diff-filetree.js', () => ({initDiffFileTree: vi.fn(), retireDiffFileTree: vi.fn()}));
vi.mock('./repo-diff-commitselect.js', () => ({initDiffCommitSelect: vi.fn(), retireDiffCommitSelect: vi.fn()}));
vi.mock('./comp/ReactionSelector.js', () => ({initCompReactionSelector: vi.fn()}));
vi.mock('./repo-issue-content.js', () => ({initRepoIssueContentHistory: vi.fn()}));
vi.mock('./comp/ComboMarkdownEditor.js', () => ({validateTextareaNonEmpty: () => true}));
vi.mock('./pull-view-file.js', () => ({initViewedCheckboxListenerFor: vi.fn(), countAndUpdateViewedFiles: vi.fn(), initExpandAndCollapseFilesButton: vi.fn(), retireViewedFiles: vi.fn()}));
vi.mock('./imagediff.js', () => ({initImageDiff: vi.fn(), retireImageDiff: vi.fn()}));
vi.mock('../modules/toast.js', () => ({showErrorToast: vi.fn()}));
vi.mock('../modules/fetch.js', () => ({GET: vi.fn(), POST: vi.fn()}));
vi.mock('htmx.org', () => ({default: {process: vi.fn()}}));

const savedPageData = window.config.pageData;

function installDiff(info) {
  document.body.innerHTML = '<div id="diff-file-boxes"><div id="diff-incomplete">original</div></div><a id="diff-show-more-files"></a>';
  setPageConfig({diffFileInfo: info});
  initRepoDiffView();
  return document.getElementById('diff-show-more-files');
}

function nextFiles() {
  return '<div><div id="diff-file-boxes"><div id="fresh-file">fresh file</div></div><script id="diff-data-script">{"files":[{"name":"fresh"}],"nextPage":3}</script></div>';
}

afterEach(() => {
  retireRepoDiffView();
  retireRepoDiffConversationNav();
  document.body.innerHTML = '';
  window.config.pageData = savedPageData;
  window.history.replaceState(null, '', '/');
  vi.restoreAllMocks();
});

test('an outgoing pagination finally clears only its captured info while incoming pagination remains loading', async () => {
  const outgoing = Promise.withResolvers();
  const incoming = Promise.withResolvers();
  GET.mockReturnValueOnce(outgoing.promise).mockReturnValueOnce(incoming.promise);
  const oldInfo = {files: []};
  const oldTarget = installDiff(oldInfo);
  const oldRequest = loadMoreFiles('/outgoing');
  const oldSignal = GET.mock.calls[0][1].signal;
  retireRepoDiffView();
  const newInfo = {files: []};
  const newTarget = installDiff(newInfo);
  const newRequest = loadMoreFiles('/incoming');
  const oldBody = vi.fn();

  outgoing.resolve({text: oldBody});
  await oldRequest;

  expect(oldSignal.aborted).toBe(true);
  expect(oldBody).not.toHaveBeenCalled();
  expect(oldInfo.isLoadingNewData).toBe(false);
  expect(newInfo.isLoadingNewData).toBe(true);
  expect(newTarget.classList.contains('disabled')).toBe(true);
  expect(oldTarget.classList.contains('disabled')).toBe(true);
  incoming.resolve({text: async () => nextFiles()});
  await newRequest;
  expect(newInfo.isLoadingNewData).toBe(false);
  expect(newInfo.files).toEqual([{name: 'fresh'}]);
  expect(newTarget.classList.contains('disabled')).toBe(false);
  expect(document.getElementById('fresh-file').textContent).toBe('fresh file');
  expect(initImageDiff).toHaveBeenCalledExactlyOnceWith();
});

test('retirement during pagination body decoding prevents late DOM and config writes', async () => {
  const body = Promise.withResolvers();
  const text = vi.fn(() => body.promise);
  GET.mockResolvedValue({text});
  const oldInfo = {files: []};
  installDiff(oldInfo);
  const request = loadMoreFiles('/outgoing');
  await Promise.resolve();
  expect(text).toHaveBeenCalledExactlyOnceWith();
  retireRepoDiffView();
  const newInfo = {files: [], isLoadingNewData: true};
  installDiff(newInfo);

  body.resolve(nextFiles());
  await request;

  expect(oldInfo.isLoadingNewData).toBe(false);
  expect(newInfo).toEqual({files: [], isLoadingNewData: true});
  expect(document.getElementById('diff-incomplete').textContent).toBe('original');
  expect(initImageDiff).not.toHaveBeenCalled();
});

test('a stale pagination error does not show an incoming-page error or clear incoming loading state', async () => {
  const body = Promise.withResolvers();
  GET.mockResolvedValue({text: () => body.promise});
  const report = vi.spyOn(console, 'error').mockImplementation(() => {});
  const oldInfo = {files: []};
  installDiff(oldInfo);
  const request = loadMoreFiles('/outgoing');
  await Promise.resolve();
  retireRepoDiffView();
  const newInfo = {files: [], isLoadingNewData: true};
  installDiff(newInfo);

  body.reject(new Error('outgoing network failure'));
  await request;

  expect(report).not.toHaveBeenCalled();
  expect(showErrorToast).not.toHaveBeenCalled();
  expect(oldInfo.isLoadingNewData).toBe(false);
  expect(newInfo.isLoadingNewData).toBe(true);
});

test('a conversation POST finishing after retirement cannot update the incoming conversation or loading classes', async () => {
  const body = Promise.withResolvers();
  POST.mockResolvedValue({text: () => body.promise});
  document.body.innerHTML = '<div id="diff-file-boxes"></div><div class="conversation-holder"><form action="/conversation"><textarea>original</textarea></form></div>';
  initRepoDiffView();
  const oldForm = document.querySelector('form');
  oldForm.dispatchEvent(new Event('submit', {bubbles: true, cancelable: true}));
  await Promise.resolve();
  retireRepoDiffView();
  document.body.innerHTML = '<div id="diff-file-boxes"></div><div class="conversation-holder"><form class="is-loading"><textarea>incoming</textarea></form></div>';
  initRepoDiffView();

  body.resolve('<div class="conversation-holder" data-path="path" data-side="left" data-idx="1">outgoing</div>');
  await Promise.resolve();
  await Promise.resolve();

  expect(POST).toHaveBeenCalledTimes(1);
  expect(document.querySelector('textarea').value).toBe('incoming');
  expect(document.querySelector('form').classList.contains('is-loading')).toBe(true);
  expect(oldForm.classList.contains('is-loading')).toBe(true);
  expect(initCompReactionSelector).not.toHaveBeenCalled();
});

test('diff-view retirement leaves separately owned image diff and conversation navigation active', () => {
  document.body.innerHTML = '<div id="diff-file-boxes"></div><div class="comment-code-cloud"><div class="comment" id="first"></div><button class="previous-conversation">previous</button></div><div class="comment-code-cloud"><div class="comment" id="last"></div></div>';
  initRepoDiffView();
  initRepoDiffConversationNav();

  retireRepoDiffView();
  document.querySelector('button').click();

  expect(window.location.hash).toBe('#last');
  expect(retireImageDiff).not.toHaveBeenCalled();
});
