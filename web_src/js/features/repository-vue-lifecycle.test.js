import {vi} from 'vitest';
import {h, onMounted, onUnmounted} from 'vue';

const entries = [
  {feature: './repo-activity-top-authors.ts', component: '../components/RepoActivityTopAuthors.vue', id: 'repo-activity-top-authors-chart', init: 'initRepoActivityTopAuthorsChart', retire: 'retireRepoActivityTopAuthorsChart'},
  {feature: './repo-action-view.ts', component: '../components/RepoActionView.vue', id: 'repo-action-view', init: 'initRepositoryActionView', retire: 'retireRepositoryActionView'},
  {feature: './contributors.js', component: '../components/RepoContributors.vue', id: 'repo-contributors-chart', init: 'initRepoContributors', retire: 'retireRepoContributors'},
  {feature: './code-frequency.js', component: '../components/RepoCodeFrequency.vue', id: 'repo-code-frequency-chart', init: 'initRepoCodeFrequency', retire: 'retireRepoCodeFrequency'},
  {feature: './recent-commits.js', component: '../components/RepoRecentCommits.vue', id: 'repo-recent-commits-chart', init: 'initRepoRecentCommits', retire: 'retireRepoRecentCommits'},
  {feature: './repo-branch-tag-selector.js', component: '../components/RepoBranchTagSelector.vue', id: 'branch-selector', init: 'initRepoBranchTagSelector', retire: 'retireRepoBranchTagSelector'},
];
const savedPageData = window.config.pageData;
let activeFeature;
let activeEntry;

function install(entry) {
  document.body.innerHTML = '<div class="full height" style="padding-bottom:11px"><div class="page-content repository"></div></div>';
  const root = document.createElement('div');
  root.id = entry.id;
  if (entry.id === 'branch-selector') root.className = 'js-branch-tag-selector';
  root.setAttribute('data-initial-post-response', '{}');
  root.setAttribute('data-initial-artifacts-response', '{}');
  root.setAttribute('data-repo-link', '/current/repository');
  document.querySelector('.page-content').append(root);
  window.config.pageData = {branchDropdownDataList: [{repoLink: '/current/repository'}]};
  return root;
}

const renderProbe = () => h('span', {class: 'repository-vue-probe'}, 'Native view');

function probe(events) {
  return {
    data: () => ({requestController: new AbortController(), retired: false}),
    setup() {
      onMounted(() => events.push('mounted'));
      onUnmounted(() => events.push('unmounted'));
      return renderProbe;
    },
  };
}

function deferred() {
  let resolve, reject;
  const promise = new Promise((accept, refuse) => { resolve = accept; reject = refuse });
  return {promise, resolve, reject};
}

beforeEach(() => {
  vi.resetModules();
  for (const entry of entries) vi.doUnmock(entry.component);
});

afterEach(() => {
  activeFeature?.[activeEntry.retire]();
  activeFeature = activeEntry = undefined;
  window.config.pageData = savedPageData;
  document.body.innerHTML = '';
});

for (const entry of entries) {
  test(`${entry.init} mounts once and unmounts through the native page lifecycle`, async () => {
    const events = [];
    vi.doMock(entry.component, () => ({default: probe(events)}));
    activeEntry = entry;
    activeFeature = await import(entry.feature);
    const {registerPageFeature, initPageFeatures, retirePageFeatures} = await import('../native-lifecycle.js');
    let pending;
    registerPageFeature(entry.id, {
      init: () => { pending = activeFeature[entry.init]() },
      retire: activeFeature[entry.retire],
    });
    const first = install(entry);
    initPageFeatures();
    await pending;
    initPageFeatures();
    await activeFeature[entry.init]();
    expect(events).toEqual(['mounted']);
    retirePageFeatures();
    expect(events).toEqual(['mounted', 'unmounted']);
    expect(first.querySelector('.repository-vue-probe')).toBeNull();
    const second = install(entry);
    initPageFeatures();
    await pending;
    expect(second.querySelector('.repository-vue-probe')).not.toBeNull();
    expect(events).toEqual(['mounted', 'unmounted', 'mounted']);
    retirePageFeatures();
    activeFeature[entry.retire]();
    expect(events).toEqual(['mounted', 'unmounted', 'mounted', 'unmounted']);
  });

  test(`${entry.init} ignores a retired import even when its root is reused`, async () => {
    const events = [];
    const loaded = deferred();
    const importer = vi.fn(() => loaded.promise);
    vi.doMock(entry.component, importer);
    activeEntry = entry;
    activeFeature = await import(entry.feature);
    const root = install(entry);
    const stale = activeFeature[entry.init]();
    await vi.waitFor(() => expect(importer).toHaveBeenCalledOnce());
    activeFeature[entry.retire]();
    const current = activeFeature[entry.init]();
    loaded.resolve({default: probe(events)});
    await Promise.all([stale, current]);
    expect(events).toEqual(['mounted']);
    expect(root.querySelectorAll('.repository-vue-probe')).toHaveLength(1);
  });

  test(`${entry.init} ignores an import failure after its owner retires`, async () => {
    const loaded = deferred();
    const importer = vi.fn(() => loaded.promise);
    vi.doMock(entry.component, importer);
    activeEntry = entry;
    activeFeature = await import(entry.feature);
    install(entry);
    const pending = activeFeature[entry.init]();
    await vi.waitFor(() => expect(importer).toHaveBeenCalledOnce());
    activeFeature[entry.retire]();
    loaded.reject(new Error('retired native import failed'));
    await expect(pending).resolves.toBeUndefined();
  });

  test(`${entry.init} never mounts a detached native root`, async () => {
    const events = [];
    const loaded = deferred();
    vi.doMock(entry.component, () => loaded.promise);
    activeEntry = entry;
    activeFeature = await import(entry.feature);
    const first = install(entry);
    const pending = activeFeature[entry.init]();
    const second = install(entry);
    loaded.resolve({default: probe(events)});
    await pending;
    expect(events).toEqual([]);
    expect(first.querySelector('.repository-vue-probe')).toBeNull();
    expect(second.querySelector('.repository-vue-probe')).toBeNull();
    await activeFeature[entry.init]();
    expect(events).toEqual(['mounted']);
  });

  test(`${entry.init} leaves contribution-owned roots untouched`, async () => {
    const events = [];
    vi.doMock(entry.component, () => ({default: probe(events)}));
    activeEntry = entry;
    activeFeature = await import(entry.feature);
    const root = install(entry);
    const contribution = document.createElement('div');
    contribution.dataset.extensionPage = '';
    root.before(contribution);
    contribution.append(root);
    root.textContent = 'Retained contribution';
    await activeFeature[entry.init]();
    expect(events).toEqual([]);
    expect(root.textContent).toBe('Retained contribution');
  });
}

test('Actions restores its page shell padding when the Vue owner retires', async () => {
  const entry = entries[1];
  vi.doMock(entry.component, () => ({default: probe([])}));
  activeEntry = entry;
  activeFeature = await import(entry.feature);
  install(entry);
  const parent = document.querySelector('.full.height');
  await activeFeature[entry.init]();
  expect(parent.style.paddingBottom).toBe('0px');
  activeFeature[entry.retire]();
  expect(parent.style.paddingBottom).toBe('11px');
});

test('branch selectors unmount every native instance and read fresh configuration on re-entry', async () => {
  const entry = entries[5];
  const events = [];
  const captured = [];
  vi.doMock(entry.component, () => ({
    default: {
      props: ['initialData'],
      data: () => ({requestController: new AbortController(), retired: false}),
      mounted() { captured.push(this.initialData.repoLink) },
      unmounted() { events.push('unmounted') },
      render() { return h('span', {class: 'repository-vue-probe'}) },
    },
  }));
  activeEntry = entry;
  activeFeature = await import(entry.feature);
  const first = install(entry);
  const second = first.cloneNode();
  second.id = 'second-branch-selector';
  first.after(second);
  window.config.pageData.branchDropdownDataList = [{repoLink: '/first/repo'}, {repoLink: '/second/repo'}];
  await activeFeature[entry.init]();
  expect(captured).toEqual(['/first/repo', '/second/repo']);
  activeFeature[entry.retire]();
  expect(events).toHaveLength(2);
  window.config.pageData.branchDropdownDataList = [{repoLink: '/fresh/repo'}, {repoLink: '/fresh/second'}];
  await activeFeature[entry.init]();
  expect(captured.slice(2)).toEqual(['/fresh/repo', '/fresh/second']);
});
