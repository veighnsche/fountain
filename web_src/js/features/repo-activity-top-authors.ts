import {contributionRootSelector, createPageFeatureRun, getPageData} from '../native-lifecycle.js';
import {createNativeVueMount} from '../native-vue.js';

const nativeMount = createNativeVueMount();

export function retireRepoActivityTopAuthorsChart() {
  nativeMount.dispose();
}

export async function initRepoActivityTopAuthorsChart(run = createPageFeatureRun()) {
  const el = document.getElementById('repo-activity-top-authors-chart');
  if (!el?.closest('.page-content') || el.closest(contributionRootSelector)) return;
  const props = {
    initialAuthors: (getPageData().repoActivityTopAuthors || []).map((author) => ({...author})),
    locale: {
      commitActivity: el.getAttribute('data-locale-commit-activity'),
    },
  };
  return nativeMount.mount([{element: el, props, current: () => Boolean(el.closest('.page-content'))}], run,
    () => import(/* webpackChunkName: "repo-activity-top-authors" */'../components/RepoActivityTopAuthors.vue'));
}
