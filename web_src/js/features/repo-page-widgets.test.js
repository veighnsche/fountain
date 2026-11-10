import $ from 'jquery';
import {vi} from 'vitest';
import {GET, POST} from '../modules/fetch.js';
import {showModal} from '../modules/modal.ts';
import {createTippy} from '../modules/tippy.js';
import {createCodemirror} from './codemirror.ts';
import {createSortable} from '../modules/sortable.js';
import {initRepoBranchButton, retireRepoBranchButton} from './repo-branch.ts';
import {initRepoEllipsisButton, retireRepoEllipsisButton, initCommitStatuses, retireCommitStatuses, initCommitNotes, retireCommitNotes} from './repo-commit.js';
import {initRepoMigration, retireRepoMigration} from './repo-migration.js';
import {initRepoMigrationStatusChecker, retireRepoMigrationStatusChecker} from './repo-migrate.js';
import {initRepoArchiveLinks, initRepoCloneLink, initRepoCommonBranchOrTagDropdown, initRepoCommonFilterSearchDropdown, retireRepoCommon} from './repo-common.js';
import {initRepoTemplateSearch, retireRepoTemplateSearch} from './repo-template.js';
import {initRepoSettingGitHook, retireRepoSettingGitHook, initRepoSettingBranches, retireRepoSettingBranches, initRepoSettingsCollaboration, retireRepoSettingsCollaboration} from './repo-settings.js';
import {initRepoProject, retireRepoProject} from './repo-projects.js';
import {initRepoTopicBar, retireRepoTopicBar} from './repo-home.js';
import {initCompReactionSelector} from './comp/ReactionSelector.js';
import {initRepositorySearch, retireRepositorySearch} from './repo-search.js';
import {setNativeNavigator} from '../native-lifecycle.js';

vi.mock('../modules/fetch.js', () => ({GET: vi.fn(), POST: vi.fn(), PUT: vi.fn(), DELETE: vi.fn()}));
vi.mock('../modules/modal.ts', () => ({showModal: vi.fn()}));
vi.mock('../modules/tippy.js', () => ({createTippy: vi.fn()}));
vi.mock('./codemirror.ts', () => ({createCodemirror: vi.fn()}));
vi.mock('../modules/sortable.js', () => ({createSortable: vi.fn()}));

function deferred() {
  let resolve;
  const promise = new Promise((accept) => { resolve = accept });
  return {promise, resolve};
}

afterEach(() => {
  for (const retire of [retireRepoBranchButton, retireRepoEllipsisButton, retireCommitStatuses, retireCommitNotes, retireRepoMigration, retireRepoMigrationStatusChecker, retireRepoCommon, retireRepoTemplateSearch, retireRepoSettingGitHook, retireRepoSettingBranches, retireRepoSettingsCollaboration, retireRepoProject, retireRepoTopicBar, retireRepositorySearch]) retire();
  setNativeNavigator(null);
  document.body.innerHTML = '';
  vi.restoreAllMocks();
  vi.unstubAllGlobals();
  vi.useRealTimers();
});

test('repository branch buttons initialize once and release the outgoing native controls', () => {
  document.body.innerHTML = '<button class="show-create-branch-modal" data-branch-from="feature/a"></button><div id="create-branch-modal"><form data-base-action="/branches/"></form><span id="modal-create-branch-from-span"></span></div>';
  const button = document.querySelector('button');
  initRepoBranchButton();
  initRepoBranchButton();
  button.click();
  expect(showModal).toHaveBeenCalledOnce();
  expect(document.querySelector('form').action).toContain('/branches/feature%2Fa');
  retireRepoBranchButton();
  button.click();
  expect(showModal).toHaveBeenCalledOnce();
  initRepoBranchButton();
  button.click();
  expect(showModal).toHaveBeenCalledTimes(2);
});

test('ellipsis and notes controls do not double-toggle or remain bound after retirement', () => {
  document.body.innerHTML = '<div><button class="js-toggle-commit-body" aria-expanded="false"></button><div class="commit-body tw-hidden"></div></div><button id="commit-notes-add-button"></button><div id="commit-notes-edit-area" class="tw-hidden"></div>';
  initRepoEllipsisButton();
  initRepoEllipsisButton();
  initCommitNotes();
  document.querySelector('.js-toggle-commit-body').click();
  document.getElementById('commit-notes-add-button').click();
  expect(document.querySelector('.js-toggle-commit-body').getAttribute('aria-expanded')).toBe('true');
  expect(document.getElementById('commit-notes-edit-area').classList.contains('tw-hidden')).toBe(false);
  retireRepoEllipsisButton();
  retireCommitNotes();
  document.querySelector('.js-toggle-commit-body').click();
  expect(document.querySelector('.js-toggle-commit-body').getAttribute('aria-expanded')).toBe('true');
});

test('commit status popups retain a single instance and destroy it on retirement', () => {
  const popup = {destroy: vi.fn()};
  createTippy.mockReturnValue(popup);
  document.body.innerHTML = '<span data-tippy="commit-statuses"></span><div>Status</div>';
  initCommitStatuses();
  initCommitStatuses();
  expect(createTippy).toHaveBeenCalledOnce();
  retireCommitStatuses();
  retireCommitStatuses();
  expect(popup.destroy).toHaveBeenCalledOnce();
});

function migrationForm() {
  document.body.innerHTML = '<select id="service_type"><option value="2" selected>Migration</option></select><input id="auth_token"><div id="migrate_items"><input type="checkbox" name="issues"></div><input id="clone_addr"><input id="repo_name">';
}

test('migration form initialization queries the new native page rather than captured startup nodes', () => {
  migrationForm();
  initRepoMigration();
  const old = document.getElementById('clone_addr');
  retireRepoMigration();
  migrationForm();
  initRepoMigration();
  const token = document.getElementById('auth_token');
  token.value = 'synthetic';
  token.dispatchEvent(new Event('input'));
  expect(document.querySelector('#migrate_items input').disabled).toBe(false);
  old.value = '/old.git';
  old.dispatchEvent(new Event('change'));
  expect(document.getElementById('repo_name').value).toBe('');
  const current = document.getElementById('clone_addr');
  current.value = '/current.git';
  current.dispatchEvent(new Event('change'));
  expect(document.getElementById('repo_name').value).toBe('current');
});

test('retiring migration polling cancels its timer and in-flight late reload', async () => {
  vi.useFakeTimers();
  document.body.innerHTML = '<div id="repo_migrating" data-migrating-task-id="task"></div><div id="repo_migrating_progress_message"></div>';
  GET.mockResolvedValue({status: 200, url: '/task', json: async () => ({status: 1, message: 'Working'})});
  initRepoMigrationStatusChecker();
  await vi.advanceTimersByTimeAsync(0);
  expect(vi.getTimerCount()).toBe(1);
  retireRepoMigrationStatusChecker();
  expect(vi.getTimerCount()).toBe(0);
  const result = deferred();
  GET.mockReturnValue(result.promise);
  const reload = vi.spyOn(window.location, 'reload');
  initRepoMigrationStatusChecker();
  const signal = GET.mock.lastCall[1].signal;
  retireRepoMigrationStatusChecker();
  result.resolve({status: 200, url: '/task', json: async () => ({status: 4})});
  await vi.advanceTimersByTimeAsync(0);
  expect(signal.aborted).toBe(true);
  expect(reload).not.toHaveBeenCalled();
  expect(vi.getTimerCount()).toBe(0);
});

test('archive preparation stops polling when its native page is retired', async () => {
  vi.useFakeTimers();
  POST.mockResolvedValue({ok: true, json: async () => ({complete: false})});
  document.body.innerHTML = '<a class="archive-link" href="/archive.zip">Download</a>';
  initRepoArchiveLinks();
  document.querySelector('a').click();
  await vi.advanceTimersByTimeAsync(0);
  expect(vi.getTimerCount()).toBe(1);
  retireRepoCommon();
  await vi.advanceTimersByTimeAsync(2000);
  expect(POST).toHaveBeenCalledOnce();
  expect(vi.getTimerCount()).toBe(0);
});

test('clone state initializes on native entry without a template-defined global and retires its buttons', () => {
  // Happy DOM does not expose Window.origin; native browsers do.
  vi.stubGlobal('origin', window.location.origin);
  localStorage.removeItem('repo-clone-protocol');
  delete window.updateCloneStates;
  document.body.innerHTML = '<button id="repo-clone-https" data-link="/synthetic/repo.git"></button><button id="repo-clone-ssh" data-link="git@synthetic:repo.git"></button><input id="repo-clone-url" class="js-clone-url"><a class="js-clone-url-editor" data-href-template="editor://{url}"></a>';
  initRepoCloneLink();
  expect(new URL(document.getElementById('repo-clone-url').value).origin).toBe(window.location.origin);
  expect(document.getElementById('repo-clone-https').classList.contains('primary')).toBe(true);
  document.getElementById('repo-clone-ssh').click();
  expect(document.getElementById('repo-clone-url').value).toBe('git@synthetic:repo.git');
  expect(document.querySelector('.js-clone-url-editor').getAttribute('href')).toBe('editor://git%40synthetic%3Arepo.git');
  retireRepoCommon();
  document.getElementById('repo-clone-https').click();
  expect(localStorage.getItem('repo-clone-protocol')).toBe('ssh');
  localStorage.removeItem('repo-clone-protocol');
});

test('repository branch filtering enters the native page with original push history semantics', () => {
  const navigate = vi.fn();
  setNativeNavigator(navigate);
  const dropdown = vi.spyOn($.fn, 'dropdown').mockReturnThis();
  document.body.innerHTML = '<div class="choose branch"><div class="dropdown"><a data-url="/synthetic/repo/compare/base"></a></div><div class="dropdown"><a data-url="/synthetic/repo/compare/head"></a></div></div><div data-extension-panel><div class="choose branch"><div class="dropdown"></div></div></div>';
  initRepoCommonFilterSearchDropdown('.choose.branch .dropdown');
  initRepoCommonFilterSearchDropdown('.choose.branch .dropdown');
  expect(dropdown).toHaveBeenCalledTimes(2);
  const options = dropdown.mock.lastCall[0];
  const $choice = $('.choose.branch a').last();
  options.onChange('', '', $choice);
  expect(navigate).toHaveBeenCalledExactlyOnceWith('/synthetic/repo/compare/head', {replace: false});
  retireRepoCommon();
  expect(dropdown).toHaveBeenCalledTimes(4);
  expect(dropdown).toHaveBeenLastCalledWith('destroy');
  options.onChange('', '', $choice);
  expect(navigate).toHaveBeenCalledOnce();
});

test('branch-or-tag dropdown switches reference lists once and retire detaches', () => {
  document.body.innerHTML = '<div class="choose branch"><div class="dropdown"><div class="scrolling reference-list-menu"></div><div id="branches" class="tw-hidden">branches</div><button class="branch-tag-item" data-target="#branches">show</button></div></div><div data-extension-panel><div class="choose branch"><div class="dropdown"><button class="branch-tag-item" data-target="#owned">owned</button></div></div></div><div id="owned" class="tw-hidden"></div>';
  initRepoCommonBranchOrTagDropdown('.choose.branch .dropdown');
  initRepoCommonBranchOrTagDropdown('.choose.branch .dropdown');
  document.querySelector('#branches').closest('.dropdown').querySelector('.branch-tag-item').click();
  expect(document.getElementById('branches').classList.contains('tw-hidden')).toBe(false);
  document.querySelector('[data-extension-panel] .branch-tag-item').click();
  expect(document.getElementById('owned').classList.contains('tw-hidden')).toBe(true);
  retireRepoCommon();
  document.getElementById('branches').classList.add('tw-hidden');
  document.querySelector('.choose.branch .branch-tag-item').click();
  expect(document.getElementById('branches').classList.contains('tw-hidden')).toBe(true);
  // re-entry rebinds fresh nodes
  document.body.innerHTML = '<div class="choose branch"><div class="dropdown"><div class="scrolling reference-list-menu"></div><div id="tags" class="tw-hidden">tags</div><button class="branch-tag-item" data-target="#tags">show</button></div></div>';
  initRepoCommonBranchOrTagDropdown('.choose.branch .dropdown');
  document.querySelector('.branch-tag-item').click();
  expect(document.getElementById('tags').classList.contains('tw-hidden')).toBe(false);
});

test('template search destroys its widget and releases owner-selection callbacks', () => {
  const dropdown = vi.spyOn($.fn, 'dropdown').mockReturnThis();
  document.body.innerHTML = '<select id="repo_template"><option value="0">None</option></select><div id="template_units"></div><div id="non_template"></div><div id="repo_template_search"></div><input id="uid" value="1">';
  initRepoTemplateSearch();
  document.getElementById('uid').dispatchEvent(new Event('change'));
  expect(dropdown).toHaveBeenCalledTimes(2);
  retireRepoTemplateSearch();
  expect(dropdown).toHaveBeenLastCalledWith('destroy');
  document.getElementById('uid').dispatchEvent(new Event('change'));
  expect(dropdown).toHaveBeenCalledTimes(3);
});

test('a git-hook editor settling after retirement is destroyed and removed from the document collection', async () => {
  const result = deferred();
  createCodemirror.mockReturnValue(result.promise);
  document.body.innerHTML = '<div class="edit githook"><span class="hook-filename">hook</span><textarea id="content"></textarea></div>';
  const view = {destroy: vi.fn(() => window.codeEditors.delete(view))};
  window.codeEditors = new Set([view]);
  const initialized = initRepoSettingGitHook();
  retireRepoSettingGitHook();
  result.resolve({view});
  await initialized;
  expect(view.destroy).toHaveBeenCalledOnce();
  expect(window.codeEditors.has(view)).toBe(false);
});

test('branch settings retirement cancels pending status matching on the outgoing page', () => {
  vi.useFakeTimers();
  document.body.innerHTML = '<div class="repository settings branches"><textarea id="status_check_contexts"></textarea><span class="status-check-matched-mark" data-status-check="build"></span></div>';
  initRepoSettingBranches();
  const contexts = document.getElementById('status_check_contexts');
  contexts.value = 'build';
  contexts.dispatchEvent(new Event('input'));
  expect(vi.getTimerCount()).toBe(1);
  retireRepoSettingBranches();
  expect(vi.getTimerCount()).toBe(0);
  contexts.dispatchEvent(new Event('input'));
  expect(vi.getTimerCount()).toBe(0);
});

test('collaboration retirement cancels hide callbacks and late access-mode errors', async () => {
  vi.useFakeTimers();
  const result = deferred();
  POST.mockReturnValue(result.promise);
  const dropdown = vi.spyOn($.fn, 'dropdown').mockReturnThis();
  document.body.innerHTML = '<div class="page-content repository"><div class="ui dropdown access-mode" data-uid="1" data-url="/collab/1" data-last-value="read"><span class="text">Read</span></div></div>';
  initRepoSettingsCollaboration();
  const options = dropdown.mock.lastCall[0];
  const pending = options.action('', 'write');
  const signal = POST.mock.lastCall[1].signal;
  options.onHide();
  expect(vi.getTimerCount()).toBe(1);
  retireRepoSettingsCollaboration();
  expect(vi.getTimerCount()).toBe(0);
  result.resolve(Promise.reject(new Error('Late request failure')));
  await pending;
  expect(signal.aborted).toBe(true);
  expect(document.querySelector('.text').textContent).toBe('Read');
});

test('project sortables settling after retirement cannot keep native drag resources alive', async () => {
  const result = deferred();
  const sortable = {destroy: vi.fn()};
  createSortable.mockReturnValue(result.promise);
  document.body.innerHTML = '<div class="repository projects"><div id="project-board"><div class="board sortable"></div></div></div>';
  initRepoProject();
  retireRepoProject();
  result.resolve(sortable);
  await result.promise;
  await Promise.resolve();
  expect(sortable.destroy).toHaveBeenCalledOnce();
});

test('project entry owns native sortables and leaves contribution boards untouched', async () => {
  const sortable = {destroy: vi.fn()};
  createSortable.mockResolvedValue(sortable);
  document.body.innerHTML = '<div class="repository projects"><div id="project-board"><div class="board sortable"></div><div data-extension-panel><div class="board sortable"></div></div></div></div><div data-extension-panel><div id="project-board"><div class="board sortable"></div></div></div>';
  initRepoProject();
  await Promise.resolve();
  expect(createSortable).toHaveBeenCalledOnce();
  expect(createSortable.mock.lastCall[0]).toBe(document.querySelector('.repository.projects .board'));
  retireRepoProject();
  expect(sortable.destroy).toHaveBeenCalledOnce();
});

test('a retired topic save cannot update the outgoing page after its response settles', async () => {
  const result = deferred();
  POST.mockReturnValue(result.promise);
  const dropdown = vi.spyOn($.fn, 'dropdown').mockReturnThis();
  document.body.innerHTML = '<div id="repo-topics"><a class="repo-topic">old</a><button id="manage_topic"></button></div><div id="topic_edit"><div class="ui dropdown"><input class="search"><input name="topics" value="new"></div><button id="cancel_topic_edit"></button><button id="save_topic" data-link="/topics"></button></div>';
  initRepoTopicBar();
  document.getElementById('save_topic').click();
  const signal = POST.mock.lastCall[1].signal;
  retireRepoTopicBar();
  result.resolve({ok: true, json: async () => ({status: 'ok'})});
  await result.promise;
  await Promise.resolve();
  expect(signal.aborted).toBe(true);
  expect(document.querySelector('.repo-topic').textContent).toBe('old');
  expect(dropdown).toHaveBeenLastCalledWith('destroy');
});

test('native reaction retirement blocks a late response and allows the same root to reenter once', async () => {
  const result = deferred();
  POST.mockReturnValue(result.promise);
  document.body.innerHTML = '<div class="comment"><div class="content" data-action-url="/reactions"><button class="comment-reaction-button" data-reaction-content="like"></button></div></div>';
  const root = document.querySelector('.comment');
  const button = root.querySelector('button');
  const first = new AbortController();
  initCompReactionSelector($(root), {signal: first.signal});
  initCompReactionSelector($(root), {signal: first.signal});
  button.click();
  expect(POST).toHaveBeenCalledOnce();
  first.abort();
  result.resolve({json: async () => ({html: '<div class="segment reactions">late</div>'})});
  await result.promise;
  await Promise.resolve();
  expect(root.querySelector('.reactions')).toBeNull();
  button.click();
  expect(POST).toHaveBeenCalledOnce();
  const second = new AbortController();
  initCompReactionSelector($(root), {signal: second.signal});
  POST.mockResolvedValue({json: async () => ({empty: true})});
  button.click();
  expect(POST).toHaveBeenCalledTimes(2);
  second.abort();
});

test('repository search uses the current native address and releases its outgoing form', () => {
  const navigate = vi.fn();
  setNativeNavigator(navigate);
  window.history.replaceState({}, '', '/synthetic/repo/search?old=1#result');
  document.body.innerHTML = '<form id="repo-search-form"><input name="q" value="current"><input name="archived" value="1"><button name="clear-filter"></button></form>';
  initRepositorySearch();
  initRepositorySearch();
  document.querySelector('button').dispatchEvent(new Event('change', {bubbles: true}));
  expect(navigate).toHaveBeenCalledOnce();
  expect(navigate.mock.lastCall[0]).toContain('/synthetic/repo/search?q=current#result');
  retireRepositorySearch();
  document.querySelector('input').dispatchEvent(new Event('change', {bubbles: true}));
  expect(navigate).toHaveBeenCalledOnce();
});
