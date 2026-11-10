import $ from 'jquery';
import {GET, POST} from '../modules/fetch.js';
import {createPageFeatureRun} from '../native-lifecycle.js';
import {getComboMarkdownEditor, initComboMarkdownEditor} from './comp/ComboMarkdownEditor.js';
import {initDropzone} from './common-global.js';
import {
  handleReply, initRepoPullRequestUpdate, retireRepoPullRequestUpdate,
  initRepoIssueWipToggle, retireRepoIssueWipToggle,
  initRepoPullRequestReview, retireRepoPullRequestReview,
  initIssueTemplateCommentEditors, initSingleCommentEditor, retireIssueTemplateCommentEditors,
  initRepoIssueComments, retireRepoIssueComments, initRepoIssueAssignMe, retireRepoIssueAssignMe,
} from './repo-issue.js';

vi.mock('../modules/fetch.js', () => ({GET: vi.fn(), POST: vi.fn()}));
vi.mock('./comp/ComboMarkdownEditor.js', () => ({getComboMarkdownEditor: vi.fn(), initComboMarkdownEditor: vi.fn()}));
vi.mock('./common-global.js', () => ({initDropzone: vi.fn()}));
vi.mock('../modules/tippy.js', () => ({createTippy: vi.fn(() => ({destroy: vi.fn(), hide: vi.fn()})), showTemporaryTooltip: vi.fn()}));
vi.mock('../modules/toast.js', () => ({showErrorToast: vi.fn()}));

async function settle() {
  await Promise.resolve();
  await Promise.resolve();
  await Promise.resolve();
}

function editor() {
  return {focus: vi.fn(), destroy: vi.fn(), switchToUserPreference: vi.fn(), value: vi.fn(() => 'edited')};
}

function installPullUpdate() {
  document.body.innerHTML = '<div class="update-button"><button data-do="/pull/update" data-redirect="/updated">update</button><div class="dropdown"></div></div>';
  initRepoPullRequestUpdate();
  return document.querySelector('button');
}

function installReply() {
  document.body.innerHTML = '<div class="comment-code-cloud"><button class="comment-form-reply">reply</button><form class="comment-form tw-hidden"><div class="dropzone"></div><div class="combo-markdown-editor"><textarea></textarea></div></form></div>';
  return $(document.querySelector('button'));
}

function installInline() {
  document.body.innerHTML = '<div class="repository pull diff"><div data-path="file" data-new-comment-url="/comment/new"><table><tbody><tr data-line-type="same"><td><button class="add-code-comment" data-side="left" data-idx="3">comment</button></td></tr></tbody></table></div></div>';
  initRepoPullRequestReview();
  document.querySelector('button').click();
}

const inlineForm = '<div class="comment-code-cloud"><div class="dropzone"></div><div class="combo-markdown-editor"><textarea></textarea></div><input name="line"><input name="side"><input name="path"></div>';

beforeEach(() => {
  window.history.replaceState(null, '', '/original');
  getComboMarkdownEditor.mockReturnValue(null);
  initDropzone.mockResolvedValue(undefined);
});

afterEach(() => {
  retireRepoPullRequestUpdate();
  retireRepoIssueWipToggle();
  retireRepoPullRequestReview();
  retireIssueTemplateCommentEditors();
  retireRepoIssueComments();
  retireRepoIssueAssignMe();
  document.body.innerHTML = '';
  window.history.replaceState(null, '', '/');
  vi.restoreAllMocks();
});

test.each(['review-request', 'assign-me'])('a retired %s metadata request cannot reload or report an error', async (feature) => {
  const pending = Promise.withResolvers();
  POST.mockReturnValue(pending.promise);
  const reload = vi.spyOn(window.location, 'reload').mockImplementation(() => {});
  const report = vi.spyOn(console, 'error').mockImplementation(() => {});
  if (feature === 'review-request') {
    document.body.innerHTML = '<div class="repository view issue"><div class="timeline"></div><button class="re-request-review" data-update-url="/reviewers" data-issue-id="7" data-id="2"></button></div>';
    initRepoIssueComments();
    document.querySelector('button').click();
    retireRepoIssueComments();
  } else {
    document.body.innerHTML = '<div class="ui assignees list"><div class="item no-select"><button class="select-assign-me" data-action="update" data-update-url="/assignees" data-issue-id="7" data-id="2"></button></div></div><div class="ui select-assignees"><div class="menu"></div></div>';
    initRepoIssueAssignMe();
    document.querySelector('button').click();
    retireRepoIssueAssignMe();
  }
  expect(POST).toHaveBeenCalledTimes(1);
  pending.reject(new Error('Retired mutation failed'));
  await settle();
  expect(reload).not.toHaveBeenCalled();
  expect(report).not.toHaveBeenCalled();
  expect(POST).toHaveBeenCalledTimes(1);
});

test.each(['review-request', 'assign-me'])('%s initialization binds replacement controls within the same page run exactly once', async (feature) => {
  const reload = vi.spyOn(window.location, 'reload').mockImplementation(() => {});
  POST.mockResolvedValue({ok: true});
  let init;
  if (feature === 'review-request') {
    document.body.innerHTML = '<div class="repository view issue"><div class="timeline"></div><button class="re-request-review" data-update-url="/reviewers" data-issue-id="7" data-id="2"></button></div>';
    init = initRepoIssueComments;
  } else {
    document.body.innerHTML = '<div class="ui assignees list"><div class="item no-select"><button class="select-assign-me" data-action="update" data-update-url="/assignees" data-issue-id="7" data-id="2"></button></div></div><div class="ui select-assignees"><div class="menu"></div></div>';
    init = initRepoIssueAssignMe;
  }
  init();
  const original = document.querySelector('button');
  original.replaceWith(original.cloneNode(true));
  init();
  init();
  document.querySelector('button').click();
  await settle();
  expect(POST).toHaveBeenCalledTimes(1);
  expect(reload).toHaveBeenCalledTimes(1);
});

test('a pull update completing after retirement cannot decode, redirect, or clear later loading state', async () => {
  const pending = Promise.withResolvers();
  POST.mockReturnValue(pending.promise);
  const reload = vi.spyOn(window.location, 'reload').mockImplementation(() => {});
  const oldButton = installPullUpdate();
  oldButton.click();
  retireRepoPullRequestUpdate();
  const incoming = installPullUpdate();
  incoming.classList.add('is-loading');
  const json = vi.fn();

  pending.resolve({json});
  await settle();

  expect(POST).toHaveBeenCalledExactlyOnceWith('/pull/update');
  expect(json).not.toHaveBeenCalled();
  expect(reload).not.toHaveBeenCalled();
  expect(window.location.pathname).toBe('/original');
  expect(oldButton.classList.contains('is-loading')).toBe(true);
  expect(incoming.classList.contains('is-loading')).toBe(true);
});

test('a pull update retired during JSON decoding cannot use either redirect', async () => {
  const body = Promise.withResolvers();
  POST.mockResolvedValue({json: () => body.promise});
  installPullUpdate().click();
  await settle();
  retireRepoPullRequestUpdate();
  installPullUpdate();

  body.resolve({redirect: '/stale-json-redirect'});
  await settle();

  expect(window.location.pathname).toBe('/original');
});

test.each(['resolve', 'reject'])('an outgoing WIP mutation does not reload or report after retirement (%s)', async (finish) => {
  const pending = Promise.withResolvers();
  POST.mockReturnValue(pending.promise);
  const reload = vi.spyOn(window.location, 'reload').mockImplementation(() => {});
  const report = vi.spyOn(console, 'error').mockImplementation(() => {});
  document.body.innerHTML = '<div class="toggle-wip" data-title="title" data-wip-prefixes=\'["WIP:"]\' data-update-url="/wip"><button>toggle</button></div>';
  initRepoIssueWipToggle();
  document.querySelector('button').click();
  retireRepoIssueWipToggle();

  pending[finish](finish === 'resolve' ? {ok: true} : new Error('outgoing failure'));
  await settle();

  expect(POST).toHaveBeenCalledTimes(1);
  expect(reload).not.toHaveBeenCalled();
  expect(report).not.toHaveBeenCalled();
});

test('a reply retired while its Dropzone is pending cannot initialize or focus an editor', async () => {
  const pending = Promise.withResolvers();
  initDropzone.mockReturnValue(pending.promise);
  const run = createPageFeatureRun();
  const initialized = handleReply(installReply(), run);
  expect(initDropzone.mock.lastCall[2]).toBe(run);
  run.dispose();

  pending.resolve();
  await initialized;

  expect(initComboMarkdownEditor).not.toHaveBeenCalled();
});

test('a reply supplies its signal before editor initialization and rejects late focus', async () => {
  const pending = Promise.withResolvers();
  initComboMarkdownEditor.mockReturnValue(pending.promise);
  const run = createPageFeatureRun();
  const initialized = handleReply(installReply(), run);
  await settle();
  expect(initComboMarkdownEditor.mock.lastCall[1].signal).toBe(run.signal);
  run.dispose();
  const late = editor();

  pending.resolve(late);
  await initialized;

  expect(late.destroy).toHaveBeenCalledExactlyOnceWith();
  expect(late.focus).not.toHaveBeenCalled();
});

test('a pending review-box editor receives the feature signal before retirement', async () => {
  const pending = Promise.withResolvers();
  initComboMarkdownEditor.mockReturnValue(pending.promise);
  document.body.innerHTML = '<div class="repository pull diff"><div><button class="js-btn-review">review</button><div class="review-box-panel"><div class="combo-markdown-editor"></div><button class="close">close</button></div></div></div>';
  const run = createPageFeatureRun();
  const initialized = initRepoPullRequestReview(run);
  expect(initComboMarkdownEditor.mock.lastCall[1].signal).toBe(run.signal);
  retireRepoPullRequestReview();
  expect(run.signal.aborted).toBe(true);
  const late = editor();

  pending.resolve(late);
  await initialized;

  expect(late.destroy).toHaveBeenCalledExactlyOnceWith();
});

test('a retired inline-comment body cannot inject an editor into a later page', async () => {
  const body = Promise.withResolvers();
  GET.mockResolvedValue({text: () => body.promise});
  installInline();
  await settle();
  retireRepoPullRequestReview();
  document.body.innerHTML = '<div id="incoming">incoming</div>';

  body.resolve(inlineForm);
  await settle();

  expect(document.body.textContent).toBe('incoming');
  expect(initDropzone).not.toHaveBeenCalled();
  expect(initComboMarkdownEditor).not.toHaveBeenCalled();
});

test('a late inline editor is destroyed without focus after the review run retires', async () => {
  const pending = Promise.withResolvers();
  GET.mockResolvedValue({text: async () => inlineForm});
  initComboMarkdownEditor.mockReturnValue(pending.promise);
  installInline();
  await vi.waitFor(() => expect(initComboMarkdownEditor).toHaveBeenCalledTimes(1));
  const signal = initComboMarkdownEditor.mock.lastCall[1].signal;
  const run = initDropzone.mock.lastCall[2];
  expect(signal).toBe(run.signal);
  retireRepoPullRequestReview();
  const late = editor();

  pending.resolve(late);
  await settle();

  expect(signal.aborted).toBe(true);
  expect(late.destroy).toHaveBeenCalledExactlyOnceWith();
  expect(late.focus).not.toHaveBeenCalled();
});

test('a pending template editor cannot bind field focus or update a retired field', async () => {
  const pending = Promise.withResolvers();
  initComboMarkdownEditor.mockReturnValue(pending.promise);
  document.body.innerHTML = '<form><div class="combo-editor-dropzone"><input class="form-field-real" value="original"><div class="form-field-dropzone"></div><div class="combo-markdown-editor"></div></div></form>';
  const run = createPageFeatureRun();
  const initialized = initIssueTemplateCommentEditors($('form'), run);
  const options = initComboMarkdownEditor.mock.lastCall[1];
  const field = document.querySelector('input');
  retireIssueTemplateCommentEditors();
  const late = editor();

  options.onContentChanged(late);
  pending.resolve(late);
  await initialized;
  $(field).triggerHandler('focus');

  expect(options.signal.aborted).toBe(true);
  expect(field.value).toBe('original');
  expect(late.switchToUserPreference).not.toHaveBeenCalled();
  expect(late.destroy).toHaveBeenCalledExactlyOnceWith();
});

test('template editor mode switching cannot focus after its owner retires', async () => {
  const switched = Promise.withResolvers();
  const active = editor();
  active.switchToUserPreference.mockReturnValue(switched.promise);
  initComboMarkdownEditor.mockResolvedValue(active);
  document.body.innerHTML = '<form><div class="combo-editor-dropzone"><input class="form-field-real"><div class="form-field-dropzone"></div><div class="combo-markdown-editor"></div></div></form>';
  await initIssueTemplateCommentEditors($('form'));
  $('.form-field-real').triggerHandler('focus');
  retireIssueTemplateCommentEditors();

  switched.resolve();
  await settle();

  expect(active.focus).not.toHaveBeenCalled();
});

test('single-comment editor callbacks cannot update retired status controls', async () => {
  const pending = Promise.withResolvers();
  initComboMarkdownEditor.mockReturnValue(pending.promise);
  document.body.innerHTML = '<form><div class="combo-markdown-editor"></div></form><button id="status-button" data-status="close" data-status-and-comment="comment and close"><span>original</span></button>';
  const run = createPageFeatureRun();
  const initialized = initSingleCommentEditor($('form'), run);
  const options = initComboMarkdownEditor.mock.lastCall[1];
  retireIssueTemplateCommentEditors();
  const late = editor();

  options.onContentChanged(late);
  pending.resolve(late);
  await initialized;

  expect(options.signal.aborted).toBe(true);
  expect(document.querySelector('#status-button span').textContent).toBe('original');
  expect(late.destroy).toHaveBeenCalledExactlyOnceWith();
});
