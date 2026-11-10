import $ from 'jquery';
import {POST} from '../modules/fetch.js';
import {createPageFeatureRun} from '../native-lifecycle.js';
import {getComboMarkdownEditor, initComboMarkdownEditor} from './comp/ComboMarkdownEditor.js';
import {initDropzone} from './common-global.js';
import {initMarkupContent} from '../markup/content.js';
import {handleReply} from './repo-issue.js';
import {MarkdownQuote} from '@github/quote-selection';

// eslint-disable-next-line no-jquery/variable-pattern
globalThis.jQuery = $;
vi.mock('../htmx.js', () => ({}));
vi.mock('htmx.org', () => ({default: {process: vi.fn(), config: {}}}));
vi.mock('../modules/fetch.js', () => ({GET: vi.fn(), POST: vi.fn()}));
vi.mock('./comp/ComboMarkdownEditor.js', () => ({getComboMarkdownEditor: vi.fn(), initComboMarkdownEditor: vi.fn()}));
vi.mock('./common-global.js', () => ({initDropzone: vi.fn(), initDisabledInputs: vi.fn(), initGlobalShowModal: vi.fn()}));
vi.mock('./comp/LabelEdit.js', () => ({initCompLabelEdit: vi.fn(), retireCompLabelEdit: vi.fn()}));
vi.mock('./comp/ReactionSelector.js', () => ({initCompReactionSelector: vi.fn()}));
vi.mock('../markup/content.js', () => ({initMarkupContent: vi.fn(), initCommentContent: vi.fn()}));
vi.mock('./contextpopup.js', () => ({attachRefIssueContextPopup: vi.fn()}));
vi.mock('./repo-issue.js', async (importOriginal) => ({...await importOriginal(), handleReply: vi.fn()}));
vi.mock('@github/quote-selection', () => ({MarkdownQuote: vi.fn(function () {
  this.range = {collapsed: true};
  this.select = vi.fn();
  this.closest = () => true;
  this.insert = vi.fn();
})}));

const {initRepository, retireRepository, initRepoCommentForm, retireRepoCommentForm} = await import('./repo-legacy.js');
const savedConfirm = window.confirm;

function editor() {
  return {value: vi.fn(() => 'draft'), focus: vi.fn(), destroy: vi.fn()};
}

async function settle() {
  await Promise.resolve();
  await Promise.resolve();
  await Promise.resolve();
}

function installEditable(run) {
  document.body.innerHTML = '<div class="page-content repository"><div class="repository view issue"><div class="header"><button class="edit-content">edit</button></div><div class="segment"><div class="edit-content-zone" data-update-url="/comment/update"></div><div class="render-content">original rendered</div><div class="raw-content">original raw</div></div></div></div><template id="issue-comment-editor-template"><div class="dropzone"></div><div class="combo-markdown-editor"><textarea></textarea></div><button data-button-name="cancel-edit">cancel</button><button data-button-name="save-edit">save</button></template>';
  initRepository(run);
  document.querySelector('.edit-content').click();
}

beforeEach(() => {
  getComboMarkdownEditor.mockReturnValue(null);
  initDropzone.mockResolvedValue(undefined);
  window.history.replaceState(null, '', '/');
  window.confirm = vi.fn(() => false);
});

afterEach(() => {
  retireRepoCommentForm();
  retireRepository();
  document.body.innerHTML = '';
  vi.restoreAllMocks();
  if (savedConfirm === undefined) delete window.confirm;
  else window.confirm = savedConfirm;
});

test('repository retirement aborts pending Dropzone initialization before an editor can be created', async () => {
  const pending = Promise.withResolvers();
  initDropzone.mockReturnValue(pending.promise);
  const run = createPageFeatureRun();
  installEditable(run);
  expect(initDropzone.mock.lastCall[2]).toBe(run);
  retireRepository();
  document.body.innerHTML = '<div id="incoming">incoming</div>';

  pending.resolve();
  await settle();

  expect(run.signal.aborted).toBe(true);
  expect(initComboMarkdownEditor).not.toHaveBeenCalled();
  expect(document.body.textContent).toBe('incoming');
});

test('a pending legacy editor receives its owner signal and cannot bind or focus after retirement', async () => {
  const pending = Promise.withResolvers();
  initComboMarkdownEditor.mockReturnValue(pending.promise);
  const run = createPageFeatureRun();
  installEditable(run);
  await settle();
  const options = initComboMarkdownEditor.mock.lastCall[1];
  expect(options.signal).toBe(run.signal);
  const oldSave = document.querySelector('[data-button-name="save-edit"]');
  retireRepository();
  const late = editor();

  pending.resolve(late);
  await settle();
  oldSave.click();

  expect(options.signal.aborted).toBe(true);
  expect(late.focus).not.toHaveBeenCalled();
  expect(late.destroy).toHaveBeenCalledExactlyOnceWith();
  expect(POST).not.toHaveBeenCalled();
});

test('repeated edit clicks while initialization is pending share one initialization and save binding', async () => {
  const pending = Promise.withResolvers();
  initComboMarkdownEditor.mockReturnValue(pending.promise);
  const active = editor();
  installEditable();
  document.querySelector('.edit-content').click();
  await settle();
  expect(initDropzone).toHaveBeenCalledTimes(1);
  expect(initComboMarkdownEditor).toHaveBeenCalledTimes(1);

  pending.resolve(active);
  await settle();
  POST.mockResolvedValue({status: 200, json: async () => ({content: 'saved', attachments: '', contentVersion: 2})});
  document.querySelector('[data-button-name="save-edit"]').click();
  await settle();

  expect(POST).toHaveBeenCalledTimes(1);
});

test('a save retired during JSON decoding cannot replace incoming comment content', async () => {
  const body = Promise.withResolvers();
  POST.mockResolvedValue({status: 200, json: () => body.promise});
  const active = editor();
  initComboMarkdownEditor.mockResolvedValue(active);
  installEditable();
  await settle();
  document.querySelector('[data-button-name="save-edit"]').click();
  await settle();
  retireRepository();
  document.body.innerHTML = '<div class="render-content">incoming rendered</div><div class="raw-content">incoming raw</div>';

  body.resolve({content: 'outgoing saved', attachments: '', contentVersion: 2});
  await settle();

  expect(POST).toHaveBeenCalledTimes(1);
  expect(document.querySelector('.render-content').textContent).toBe('incoming rendered');
  expect(document.querySelector('.raw-content').textContent).toBe('incoming raw');
  expect(initMarkupContent).not.toHaveBeenCalled();
});

test('comment-form initialization returns its editor promise and connects the run before it settles', async () => {
  const pending = Promise.withResolvers();
  initComboMarkdownEditor.mockReturnValue(pending.promise);
  document.body.innerHTML = '<form class="comment form"><div class="combo-markdown-editor"><textarea></textarea></div></form>';
  const run = createPageFeatureRun();
  const initialized = initRepoCommentForm(run);
  expect(initialized).toBeInstanceOf(Promise);
  expect(initComboMarkdownEditor.mock.lastCall[1].signal).toBe(run.signal);
  retireRepoCommentForm();
  const late = editor();

  pending.resolve(late);
  await initialized;

  expect(run.signal.aborted).toBe(true);
  expect(late.destroy).toHaveBeenCalledExactlyOnceWith();
});

test('a quote awaiting an inline reply cannot inject text after the repository run retires', async () => {
  const pending = Promise.withResolvers();
  handleReply.mockReturnValue(pending.promise);
  document.body.innerHTML = '<div class="page-content repository"><div class="repository view issue"><div class="comment-code-cloud"><button class="comment-form-reply">reply</button><button class="quote-reply quote-reply-diff" data-target="original" data-author="reader" data-reference-url="/comment">quote</button></div><div id="original">original content</div></div></div>';
  const run = createPageFeatureRun();
  initRepository(run);
  document.querySelector('.quote-reply').click();
  expect(handleReply.mock.lastCall[1]).toBe(run);
  retireRepository();
  const incoming = document.createElement('textarea');
  incoming.value = 'incoming draft';
  document.body.append(incoming);

  pending.resolve({textarea: incoming});
  await settle();

  expect(incoming.value).toBe('incoming draft');
  expect(MarkdownQuote.mock.instances[0].insert).not.toHaveBeenCalled();
});

test.each(['resolve', 'reject'])('a branch update completing after comment-form retirement cannot reload or report (%s)', async (finish) => {
  const pending = Promise.withResolvers();
  POST.mockReturnValue(pending.promise);
  const reload = vi.spyOn(window.location, 'reload').mockImplementation(() => {});
  const report = vi.spyOn(console, 'error').mockImplementation(() => {});
  document.body.innerHTML = '<form class="comment form"><div class="ui select-branch"><div class="reference-list-menu"><button type="button" class="item" data-id="release" data-id-selector="#selected-branch">release</button></div></div><input id="selected-branch"><input id="editing_mode" value="true"></form><form id="update_issueref_form" action="/branch/update"></form>';
  initRepoCommentForm();
  document.querySelector('.reference-list-menu .item').click();
  expect(POST).toHaveBeenCalledExactlyOnceWith('/branch/update', {data: new URLSearchParams({ref: 'release'})});
  retireRepoCommentForm();
  document.body.innerHTML = '<form class="comment form"><input id="selected-branch" value="incoming"></form>';
  initRepoCommentForm();

  pending[finish](finish === 'resolve' ? {ok: true} : new Error('outgoing branch failure'));
  await settle();

  expect(reload).not.toHaveBeenCalled();
  expect(report).not.toHaveBeenCalled();
  expect(document.getElementById('selected-branch').value).toBe('incoming');
  expect(POST).toHaveBeenCalledTimes(1);
});

function installLabelBatch() {
  let onHide;
  vi.spyOn($.fn, 'dropdown').mockImplementation(function (options) {
    if (this[0]?.classList.contains('select-label') && options?.onHide) onHide = options.onHide;
    return this;
  });
  document.body.innerHTML = '<form class="comment form"><div class="ui labels list"><span class="no-select">none</span><span class="item tw-hidden" id="label-1">one</span><span class="item tw-hidden" id="label-2">two</span></div><div class="select-label"><div class="menu" data-id="#label-ids" data-action="update" data-issue-id="5" data-update-url="/labels"><button type="button" class="item" data-id="1" data-id-selector="#label-1"><i class="octicon-check tw-invisible"></i>one</button><button type="button" class="item" data-id="2" data-id-selector="#label-2"><i class="octicon-check tw-invisible"></i>two</button></div></div><input id="label-ids"></form>';
  initRepoCommentForm();
  for (const item of document.querySelectorAll('.select-label .item')) item.click();
  return onHide;
}

test.each(['resolve', 'reject'])('retiring during a sidebar batch prevents its next mutation and incoming draft prompt (%s)', async (finish) => {
  const pending = Promise.withResolvers();
  POST.mockReturnValue(pending.promise);
  const reload = vi.spyOn(window.location, 'reload').mockImplementation(() => {});
  const confirm = window.confirm;
  const report = vi.spyOn(console, 'error').mockImplementation(() => {});
  const onHide = installLabelBatch();
  const completing = onHide();
  expect(POST).toHaveBeenCalledTimes(1);
  retireRepoCommentForm();
  document.body.innerHTML = '<form class="comment form" id="comment-form"><textarea>incoming draft that must survive</textarea></form>';
  initRepoCommentForm();

  pending[finish](finish === 'resolve' ? {ok: true} : new Error('outgoing batch failure'));
  await completing;
  await onHide();

  expect(POST).toHaveBeenCalledTimes(1);
  expect(confirm).not.toHaveBeenCalled();
  expect(reload).not.toHaveBeenCalled();
  expect(report).not.toHaveBeenCalled();
  expect(document.querySelector('textarea').value).toBe('incoming draft that must survive');
});

test('a current sidebar batch retains mutation order and its single final reload', async () => {
  POST.mockResolvedValue({ok: true});
  const reload = vi.spyOn(window.location, 'reload').mockImplementation(() => {});
  const onHide = installLabelBatch();

  await onHide();

  expect(POST.mock.calls.map(([url, {data}]) => [url, data.toString()])).toEqual([
    ['/labels', 'action=attach&issue_ids=5&id=1'],
    ['/labels', 'action=attach&issue_ids=5&id=2'],
  ]);
  expect(reload).toHaveBeenCalledExactlyOnceWith();
});

test.each([
  ['clear labels', '.select-label .no-select'],
  ['select project', '.select-project .item:not(.no-select)'],
  ['clear project', '.select-project .no-select'],
])('a deferred %s metadata update cannot reload the next comment form', async (_name, selector) => {
  const pending = Promise.withResolvers();
  POST.mockReturnValue(pending.promise);
  const reload = vi.spyOn(window.location, 'reload').mockImplementation(() => {});
  const confirm = window.confirm;
  document.body.innerHTML = '<form class="comment form"><div class="ui labels list"><span class="no-select">none</span></div><div class="select-label"><div class="menu" data-action="update" data-issue-id="5" data-update-url="/labels" data-id="#label-ids"><button type="button" class="item no-select">clear labels</button></div></div><input id="label-ids"><div class="select-project"><div class="menu" data-action="update" data-issue-id="5" data-update-url="/project"><button type="button" class="item" data-id="7">project</button><button type="button" class="no-select item" data-id="0">clear project</button></div></div><input id="project_id"></form>';
  initRepoCommentForm();
  document.querySelector(selector).click();
  expect(POST).toHaveBeenCalledTimes(1);
  retireRepoCommentForm();
  document.body.innerHTML = '<form class="comment form" id="comment-form"><textarea>incoming draft that must survive</textarea></form>';
  initRepoCommentForm();

  pending.resolve({ok: true});
  await settle();

  expect(POST).toHaveBeenCalledTimes(1);
  expect(confirm).not.toHaveBeenCalled();
  expect(reload).not.toHaveBeenCalled();
});
