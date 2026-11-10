import {vi} from 'vitest';
import {h, onMounted, onUnmounted} from 'vue';
import {createFeatureRun} from '../native-feature-run.js';

const pageData = window.config.pageData;
let retire;

function deferred() {
  let resolve, reject;
  const promise = new Promise((accept, refuse) => { resolve = accept; reject = refuse });
  return {promise, resolve, reject};
}

function installRoot(id) {
  document.body.innerHTML = `<div class="page-content"><div id="${id}"></div></div>`;
  return document.getElementById(id);
}

function propProbe(props, capture) {
  return {
    props,
    mounted() { capture(this) },
    render: () => h('span', {class: 'captured-vue'}, 'Native view'),
  };
}

const renderRetired = () => h('span', 'Retired during mount');

beforeEach(() => vi.resetModules());

afterEach(() => {
  retire?.();
  retire = undefined;
  document.body.replaceChildren();
  window.config.pageData = pageData;
  window.history.replaceState(null, '', '/');
  for (const component of ['RepoCodeFrequency', 'DashboardRepoList', 'RepoActivityTopAuthors', 'RepoBranchTagSelector']) vi.doUnmock(`../components/${component}.vue`);
});

test('a graph entry captures its repository and locale before a deferred component import', async () => {
  const loaded = deferred();
  const importer = vi.fn(() => loaded.promise);
  vi.doMock('../components/RepoCodeFrequency.vue', importer);
  const feature = await import('./code-frequency.js');
  retire = feature.retireRepoCodeFrequency;
  const root = installRoot('repo-code-frequency-chart');
  root.dataset.localeLoadingTitle = 'Original locale';
  window.config.pageData = {repoLink: '/original/repository'};
  const pending = feature.initRepoCodeFrequency();
  await vi.waitFor(() => expect(importer).toHaveBeenCalledOnce());
  root.dataset.localeLoadingTitle = 'Incoming locale';
  window.config.pageData = {repoLink: '/incoming/repository'};
  const capture = vi.fn();
  loaded.resolve({default: propProbe(['repoLink', 'locale'], (view) => capture(view.repoLink, view.locale.loadingTitle))});
  await pending;
  expect(capture).toHaveBeenCalledExactlyOnceWith('/original/repository', 'Original locale');
});

test('dashboard data and query belong to the originating initialization', async () => {
  const loaded = deferred();
  const importer = vi.fn(() => loaded.promise);
  vi.doMock('../components/DashboardRepoList.vue', importer);
  const feature = await import('./dashboard-repo-list.ts');
  retire = feature.retireDashboardRepoList;
  installRoot('dashboard-repo-list');
  window.history.replaceState(null, '', '/?repo-search-query=original');
  window.config.pageData = {dashboardRepoList: {uid: 11}};
  const pending = feature.initDashboardRepoList();
  await vi.waitFor(() => expect(importer).toHaveBeenCalledOnce());
  window.config.pageData = {dashboardRepoList: {uid: 22}};
  window.history.replaceState(null, '', '/?repo-search-query=incoming');
  const capture = vi.fn();
  loaded.resolve({default: propProbe(['initialData', 'locationSearch'], (view) => capture(view.initialData.uid, view.locationSearch))});
  await pending;
  expect(capture).toHaveBeenCalledExactlyOnceWith(11, '?repo-search-query=original');
});

test('activity authors are detached from page-data replacements during lazy import', async () => {
  const loaded = deferred();
  const importer = vi.fn(() => loaded.promise);
  vi.doMock('../components/RepoActivityTopAuthors.vue', importer);
  const feature = await import('./repo-activity-top-authors.ts');
  retire = feature.retireRepoActivityTopAuthorsChart;
  installRoot('repo-activity-top-authors-chart');
  const original = [{name: 'Original', commits: 3}];
  window.config.pageData = {repoActivityTopAuthors: original};
  const pending = feature.initRepoActivityTopAuthorsChart();
  await vi.waitFor(() => expect(importer).toHaveBeenCalledOnce());
  original[0].name = 'Mutated';
  window.config.pageData = {repoActivityTopAuthors: [{name: 'Incoming', commits: 4}]};
  const capture = vi.fn();
  loaded.resolve({default: propProbe(['initialAuthors'], (view) => capture(view.initialAuthors[0].name))});
  await pending;
  expect(capture).toHaveBeenCalledExactlyOnceWith('Original');
});

test('registry-run disposal retires a pending import without requiring the feature retire facade', async () => {
  const loaded = deferred();
  const importer = vi.fn(() => loaded.promise);
  vi.doMock('../components/RepoCodeFrequency.vue', importer);
  const feature = await import('./code-frequency.js');
  retire = feature.retireRepoCodeFrequency;
  const root = installRoot('repo-code-frequency-chart');
  const run = createFeatureRun();
  const pending = feature.initRepoCodeFrequency(run);
  await vi.waitFor(() => expect(importer).toHaveBeenCalledOnce());
  run.dispose();
  const mounted = vi.fn();
  loaded.resolve({default: propProbe([], mounted)});
  await pending;
  expect(mounted).not.toHaveBeenCalled();
  expect(root.childNodes).toHaveLength(0);
});

test('a run retired inside Vue mounting unmounts the completed app exactly once', async () => {
  const run = createFeatureRun();
  const unmounted = vi.fn();
  vi.doMock('../components/RepoCodeFrequency.vue', () => ({default: {
    setup() {
      onMounted(() => run.dispose());
      onUnmounted(unmounted);
      return renderRetired;
    },
  }}));
  const feature = await import('./code-frequency.js');
  retire = feature.retireRepoCodeFrequency;
  const root = installRoot('repo-code-frequency-chart');
  await feature.initRepoCodeFrequency(run);
  expect(unmounted).toHaveBeenCalledOnce();
  expect(root.childNodes).toHaveLength(0);
  feature.retireRepoCodeFrequency();
  expect(unmounted).toHaveBeenCalledOnce();
});

test('branch selector initialization is a supported captured prop for every native instance', async () => {
  const loaded = deferred();
  const importer = vi.fn(() => loaded.promise);
  vi.doMock('../components/RepoBranchTagSelector.vue', importer);
  const feature = await import('./repo-branch-tag-selector.js');
  retire = feature.retireRepoBranchTagSelector;
  const first = installRoot('first-selector');
  first.className = 'js-branch-tag-selector';
  const second = first.cloneNode();
  second.id = 'second-selector';
  first.after(second);
  const configuration = [{repoLink: '/original/one'}, {repoLink: '/original/two'}];
  window.config.pageData = {branchDropdownDataList: configuration};
  const pending = feature.initRepoBranchTagSelector();
  await vi.waitFor(() => expect(importer).toHaveBeenCalledOnce());
  configuration[0].repoLink = '/mutated/one';
  window.config.pageData = {branchDropdownDataList: [{repoLink: '/incoming/one'}]};
  const capture = vi.fn();
  loaded.resolve({default: propProbe(['initialData'], (view) => capture(view.initialData.repoLink))});
  await pending;
  expect(capture.mock.calls).toEqual([['/original/one'], ['/original/two']]);
});
