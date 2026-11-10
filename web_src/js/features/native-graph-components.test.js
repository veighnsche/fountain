import {vi} from 'vitest';
import {createApp, nextTick} from 'vue';
import {GET} from '../modules/fetch.js';

vi.mock('../modules/fetch.js', () => ({GET: vi.fn()}));
vi.mock('../svg.js', () => ({SvgIcon: {render: () => null}}));
vi.mock('vue-chartjs', () => ({Line: {render: () => null}, Bar: {render: () => null}}));

let app;

function deferred() {
  let resolve;
  const promise = new Promise((accept) => { resolve = accept });
  return {promise, resolve};
}

function mountView(component, props) {
  const root = document.createElement('div');
  document.body.append(root);
  app = createApp(component, props);
  return {root, view: app.mount(root)};
}

beforeEach(() => GET.mockReset());

afterEach(() => {
  app?.unmount();
  app = undefined;
  document.body.replaceChildren();
});

test.each(['RepoCodeFrequency', 'RepoContributors', 'RepoRecentCommits'])('%s retires its body read with its Vue lifetime', async (name) => {
  const body = deferred();
  const json = vi.fn(() => body.promise);
  GET.mockResolvedValue({status: 200, ok: true, json});
  const {default: component} = await import(`../components/${name}.vue`);
  const {view} = mountView(component, {repoLink: '/captured/repo', repoDefaultBranchName: 'main', locale: {
    loadingTitle: 'Loading', loadingTitleFailed: 'Unavailable', loadingInfo: 'Preparing',
    codeFrequencyTitle: 'Changes in {0}', contributionType: {commits: 'Commits'},
  }});
  await vi.waitFor(() => expect(json).toHaveBeenCalledOnce());
  const signal = GET.mock.calls[0][1].signal;
  const previousData = name === 'RepoContributors' ? view.totalStats : view.data;
  app.unmount();
  app = undefined;
  expect(signal.aborted).toBe(true);
  body.resolve({invalid: 'a late response must never reach the transform'});
  await body.promise;
  await nextTick();
  expect(view.errorText).toBe('');
  expect(name === 'RepoContributors' ? view.totalStats : view.data).toEqual(previousData);
  expect(GET).toHaveBeenCalledOnce();
});

test('the real branch selector owns its defaults and accepts captured initialization props', async () => {
  GET.mockResolvedValue({json: async () => ({results: ['main']})});
  const {default: component} = await import('../components/RepoBranchTagSelector.vue');
  const {root, view} = mountView(component, {initialData: {
    repoLink: '/captured/repo', viewType: 'branch', branchName: 'main', mode: 'branches',
    defaultSelectedRefName: 'main', showBranchesInDropdown: true,
  }});
  expect(root.querySelector('strong').textContent).toBe('main');
  expect(view.items).toEqual([]);
  expect(view.active).toBe(0);
  view.menuVisible = true;
  await vi.waitFor(() => expect(view.items).toHaveLength(1));
  expect(GET.mock.calls[0][0]).toBe('/captured/repo/branches/list');
  expect(view.items[0].selected).toBe(true);
  const signal = GET.mock.calls[0][1].signal;
  app.unmount();
  app = undefined;
  expect(signal.aborted).toBe(true);
});
