import {vi} from 'vitest';
import {GET} from '../modules/fetch.js';
import {retirePageFeatures} from '../native-lifecycle.js';
import {initRepoIssueContentHistory, retireRepoIssueContentHistory} from './repo-issue-content.js';

vi.mock('../modules/fetch.js', () => ({GET: vi.fn(), POST: vi.fn()}));

function overviewResponse() {
  return {
    json: async () => ({
      i18n: {textEdited: 'Edited', textDeleteFromHistory: 'Delete', textDeleteFromHistoryConfirm: 'Sure?', textOptions: 'Options'},
      editedHistoryCountMap: {0: 1, 5: 2},
    }),
  };
}

function installIssuePage() {
  document.body.innerHTML = `<input type="hidden" id="issueIndex" value="7">
  <input type="hidden" id="repolink" value="owner/repo">
  <div class="repository issue">
    <div class="timeline-item comment first"><div class="comment-header-left"></div></div>
    <div class="comment-list">
      <div class="comment" id="issuecomment-5"><div class="comment-header-left"></div></div>
    </div>
  </div>`;
}

afterEach(() => {
  retireRepoIssueContentHistory();
  document.body.innerHTML = '';
});

test('history menus are built from the current repository context and removed on retire', async () => {
  installIssuePage();
  GET.mockResolvedValue(overviewResponse());
  await initRepoIssueContentHistory();
  expect(GET).toHaveBeenCalledWith('/owner/repo/issues/7/content-history/overview', {signal: expect.any(AbortSignal)});
  expect(document.querySelectorAll('.content-history-menu')).toHaveLength(2);
  retireRepoIssueContentHistory();
  expect(document.querySelectorAll('.content-history-menu')).toHaveLength(0);
});

test('an overview that resolves after retirement builds no menus', async () => {
  installIssuePage();
  let resolveGet;
  GET.mockImplementation(() => new Promise((resolve) => { resolveGet = resolve }));
  const pending = initRepoIssueContentHistory();
  retirePageFeatures();
  resolveGet(overviewResponse());
  await pending;
  expect(document.querySelectorAll('.content-history-menu')).toHaveLength(0);
});
