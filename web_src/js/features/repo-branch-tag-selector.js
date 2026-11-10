import {contributionRootSelector, createPageFeatureRun} from '../native-lifecycle.js';
import {createNativeVueMount} from '../native-vue.js';

const nativeMount = createNativeVueMount();

export function retireRepoBranchTagSelector() {
  nativeMount.dispose();
}

export async function initRepoBranchTagSelector(run = createPageFeatureRun()) {
  const roots = Array.from(document.querySelectorAll('.js-branch-tag-selector'))
    .filter((root) => root.closest('.page-content') && !root.closest(contributionRootSelector));
  const data = window.config.pageData.branchDropdownDataList;
  const mounts = roots.map((element, index) => ({
    element,
    props: {initialData: {...data?.[index]}},
    current: () => Boolean(element.closest('.page-content')),
  }));
  return nativeMount.mount(mounts, run,
    () => import(/* webpackChunkName: "repo-branch-tag-selector" */'../components/RepoBranchTagSelector.vue'));
}
