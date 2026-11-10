import {vi} from 'vitest';
import {updateIssuesMeta} from './repo-issue.js';
import {createSortable} from '../modules/sortable.js';
import {initRepoIssueList, retireRepoIssueList} from './repo-issue-list.js';

vi.mock('./repo-issue.js', () => ({updateIssuesMeta: vi.fn(async () => {})}));
vi.mock('./comp/ConfirmModal.js', () => ({confirmModal: vi.fn(async () => true)}));
vi.mock('../modules/toast.js', () => ({showErrorToast: vi.fn()}));
vi.mock('../modules/fetch.js', () => ({DELETE: vi.fn(), POST: vi.fn()}));
vi.mock('../modules/sortable.js', () => ({createSortable: vi.fn(async () => ({destroy: vi.fn()}))}));

function installIssueList() {
  document.body.innerHTML = `<div class="page-content repository issue-list">
    <div id="issue-filters"><div class="issue-list-toolbar-left"></div></div>
    <div id="issue-actions" class="tw-hidden"><div class="issue-list-toolbar-left"></div></div>
    <input type="checkbox" class="issue-checkbox-all">
    <input type="checkbox" class="issue-checkbox" data-issue-id="1">
    <input type="checkbox" class="issue-checkbox" data-issue-id="2">
    <button class="issue-action" data-url="/issues/action" data-action="close" data-element-id="9">Close</button>
  </div>`;
}

afterEach(() => {
  retireRepoIssueList();
  document.body.innerHTML = '';
  window.history.replaceState(null, '', '/');
});

test('selection syncs panels and bulk action posts the checked ids once', async () => {
  installIssueList();
  initRepoIssueList();
  initRepoIssueList();
  const [first] = document.querySelectorAll('.issue-checkbox');
  first.checked = true;
  first.dispatchEvent(new Event('change'));
  expect(document.getElementById('issue-filters').classList.contains('tw-hidden')).toBe(true);
  expect(document.getElementById('issue-actions').classList.contains('tw-hidden')).toBe(false);

  document.querySelector('.issue-action').click();
  await vi.waitFor(() => expect(updateIssuesMeta).toHaveBeenCalledTimes(1));
  expect(updateIssuesMeta).toHaveBeenCalledWith('/issues/action', 'close', '1', '9', expect.objectContaining({signal: expect.any(AbortSignal), current: expect.any(Function)}));
});

test('retire detaches list handlers', async () => {
  installIssueList();
  initRepoIssueList();
  retireRepoIssueList();
  const [first] = document.querySelectorAll('.issue-checkbox');
  first.checked = true;
  first.dispatchEvent(new Event('change'));
  expect(document.getElementById('issue-actions').classList.contains('tw-hidden')).toBe(true);
  document.querySelector('.issue-action').click();
  await new Promise((resolve) => setTimeout(resolve, 0));
  expect(updateIssuesMeta).not.toHaveBeenCalled();
});

test('pin sortable is created for admins and destroyed on retire', async () => {
  document.body.innerHTML = `<div class="page-content repository issue-list">
    <div id="issue-pins" data-is-repo-admin="true"><div>one</div><div>two</div></div>
  </div>`;
  initRepoIssueList();
  await vi.waitFor(() => expect(createSortable).toHaveBeenCalledTimes(1));
  const instance = await createSortable.mock.results[0].value;
  retireRepoIssueList();
  expect(instance.destroy).toHaveBeenCalledTimes(1);
});

test('archived label filter navigates with the current query state', () => {
  document.body.innerHTML = `<div class="page-content repository issue-list">
    <input type="checkbox" id="archived-filter-checkbox">
    <div class="item"><span data-is-archived="true" data-label-id="3">old</span></div>
  </div>`;
  window.history.replaceState(null, '', '/issues?labels=1');
  initRepoIssueList();
  const box = document.getElementById('archived-filter-checkbox');
  box.checked = true;
  box.dispatchEvent(new Event('change'));
  expect(window.location.search).toContain('archived=true');
});
