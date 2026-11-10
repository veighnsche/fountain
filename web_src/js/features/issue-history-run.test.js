import $ from 'jquery';
import {vi} from 'vitest';
import {GET, POST} from '../modules/fetch.js';
import {showErrorToast} from '../modules/toast.js';
import {retirePageFeatures} from '../native-lifecycle.js';
import {initRepoIssueContentHistory, retireRepoIssueContentHistory} from './repo-issue-content.js';

vi.mock('../modules/fetch.js', () => ({GET: vi.fn(), POST: vi.fn()}));
vi.mock('../modules/toast.js', () => ({showErrorToast: vi.fn()}));

const dropdownOptions = new Map();
const modalOptions = new Map();
let modal;

function deferred() {
  let resolve;
  const promise = new Promise((done) => { resolve = done });
  return {promise, resolve};
}

function overview(label = 'Current') {
  return {
    i18n: {textEdited: `${label} edited`, textOptions: `${label} options`, textDeleteFromHistory: `${label} delete`, textDeleteFromHistoryConfirm: `${label} sure?`},
    editedHistoryCountMap: {0: 1, 5: 2},
  };
}

function installIssuePage() {
  document.body.innerHTML = `<div class="page-content">
    <input type="hidden" id="issueIndex" value="7"><input type="hidden" id="repolink" value="owner/repo">
    <div class="repository issue">
      <div class="timeline-item comment first"><div class="comment-header-left"></div></div>
      <div class="comment-list"><div class="comment" id="issuecomment-5"><div class="comment-header-left"></div></div></div>
    </div>
    <div data-extension-panel id="contribution-history">
      <div class="content-history-menu">Owner menu</div>
      <div class="content-history-detail-dialog">Owner dialog</div>
    </div>
  </div>`;
}

function openDetail(historyId = '10') {
  const menu = document.querySelector('.repository.issue .content-history-menu');
  dropdownOptions.get(menu).onChange(historyId, 'History entry', $('<div>'));
  return document.querySelector('body > .content-history-detail-dialog');
}

beforeEach(() => {
  GET.mockReset();
  POST.mockReset();
  showErrorToast.mockReset();
  dropdownOptions.clear();
  modalOptions.clear();
  vi.spyOn($.fn, 'dropdown').mockImplementation(function (options) {
    if (typeof options === 'object') for (const element of this) dropdownOptions.set(element, options);
    return this;
  });
  modal = vi.fn(function (options) {
    if (typeof options === 'object') for (const element of this) modalOptions.set(element, options);
    return this;
  });
  $.fn.modal = modal;
});

afterEach(() => {
  retireRepoIssueContentHistory();
  retirePageFeatures();
  document.body.innerHTML = '';
  delete $.fn.modal;
  delete window.confirm;
  vi.restoreAllMocks();
});

test('an old overview body cannot bind fresh same-ID comments or replace their detail translations', async () => {
  installIssuePage();
  const oldBody = deferred();
  const oldJSON = vi.fn(() => oldBody.promise);
  GET.mockResolvedValueOnce({json: oldJSON});
  const oldOverview = initRepoIssueContentHistory();
  await Promise.resolve();
  expect(oldJSON).toHaveBeenCalledTimes(1);

  retireRepoIssueContentHistory();
  retirePageFeatures();
  installIssuePage();
  GET.mockResolvedValueOnce({json: async () => overview('Fresh')});
  await initRepoIssueContentHistory();
  oldBody.resolve(overview('Stale'));
  await oldOverview;

  const menus = document.querySelectorAll('.repository.issue .content-history-menu');
  expect(menus).toHaveLength(2);
  for (const menu of menus) expect(menu.textContent).toContain('Fresh edited');
  const dialog = openDetail();
  expect(dialog.querySelector('[data-option-item="delete"]').textContent).toBe('Fresh delete');
  expect(dialog.querySelector('.dialog-header-options').textContent).toContain('Fresh options');
});

test('closing a history dialog invalidates its pending JSON before another dialog is opened', async () => {
  installIssuePage();
  GET.mockResolvedValueOnce({json: async () => overview()});
  await initRepoIssueContentHistory();
  const oldDialog = openDetail();
  const oldDiff = oldDialog.querySelector('.comment-diff-data');
  const body = deferred();
  const json = vi.fn(() => body.promise);
  GET.mockResolvedValueOnce({json});
  const read = modalOptions.get(oldDialog).onShow();
  await Promise.resolve();
  expect(json).toHaveBeenCalledTimes(1);
  modalOptions.get(oldDialog).onHidden();
  expect(oldDialog.isConnected).toBe(false);
  const newDialog = openDetail('11');
  const newHTML = newDialog.innerHTML;
  const callsAfterClose = modal.mock.calls.length;

  body.resolve({diffHtml: '<p>Retired diff</p>', canSoftDelete: true});
  await read;
  expect(oldDiff.innerHTML).toBe('');
  expect(oldDiff.classList.contains('is-loading')).toBe(true);
  expect(newDialog.innerHTML).toBe(newHTML);
  expect(modal).toHaveBeenCalledTimes(callsAfterClose);
  expect(showErrorToast).not.toHaveBeenCalled();
});

test('retiring a pending history deletion owns only native menus and dialogs and suppresses its late error body', async () => {
  installIssuePage();
  const owner = document.querySelector('#contribution-history');
  const ownerHTML = owner.innerHTML;
  GET.mockResolvedValueOnce({json: async () => overview()});
  await initRepoIssueContentHistory();
  const nativeMenus = Array.from(document.querySelectorAll('.repository.issue .content-history-menu'));
  const dialog = openDetail();
  const options = dialog.querySelector('.dialog-header-options');
  const body = deferred();
  const json = vi.fn(() => body.promise);
  POST.mockResolvedValue({json});
  window.confirm = vi.fn(() => true);
  const deletion = dropdownOptions.get(options).onChange('', '', $(options.querySelector('[data-option-item="delete"]')));
  await Promise.resolve();
  expect(json).toHaveBeenCalledTimes(1);
  expect(POST).toHaveBeenCalledExactlyOnceWith('/owner/repo/issues/7/content-history/soft-delete?comment_id=0&history_id=10');

  retireRepoIssueContentHistory();
  retirePageFeatures();
  expect(nativeMenus.every((menu) => !menu.isConnected)).toBe(true);
  expect(dialog.isConnected).toBe(false);
  expect(owner.isConnected).toBe(true);
  expect(owner.innerHTML).toBe(ownerHTML);
  const afterRetirement = document.body.innerHTML;
  const modalCalls = modal.mock.calls.length;

  body.resolve({ok: false, message: 'Retired deletion error'});
  await deletion;
  expect(document.body.innerHTML).toBe(afterRetirement);
  expect(modal).toHaveBeenCalledTimes(modalCalls);
  expect(showErrorToast).not.toHaveBeenCalled();
  expect(POST).toHaveBeenCalledTimes(1);
});
