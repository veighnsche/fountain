import {contributionRootSelector, createPageFeatureRun, getPageData} from '../native-lifecycle.js';
import {createNativeVueMount} from '../native-vue.js';

const nativeMount = createNativeVueMount();

export function retireDashboardRepoList() {
  nativeMount.dispose();
}

export async function initDashboardRepoList(run = createPageFeatureRun()) {
  const el = document.getElementById('dashboard-repo-list');
  if (!el?.closest('.page-content') || el.closest(contributionRootSelector)) return;
  const props = {
    initialData: {...getPageData().dashboardRepoList},
    locationSearch: window.location.search,
  };
  return nativeMount.mount([{element: el, props, current: () => Boolean(el.closest('.page-content'))}], run,
    () => import(/* webpackChunkName: "dashboard-repo-list" */'../components/DashboardRepoList.vue'));
}
