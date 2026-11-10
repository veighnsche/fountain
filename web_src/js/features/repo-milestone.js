import {initComboMarkdownEditor} from './comp/ComboMarkdownEditor.js';
import {contributionRootSelector, createPageFeatureRun} from '../native-lifecycle.js';

let milestoneRun = null;

export function retireRepoMilestoneEditor() {
  const run = milestoneRun;
  milestoneRun = null;
  run?.dispose();
}

export async function initRepoMilestoneEditor(run = createPageFeatureRun()) {
  retireRepoMilestoneEditor();
  milestoneRun = run;
  const editor = document.querySelector('.page-content.repository.milestone .combo-markdown-editor');
  if (!editor || editor.closest(contributionRootSelector)) {
    return;
  }
  try {
    const instance = await initComboMarkdownEditor(editor, {signal: run.signal});
    const release = run.own(() => instance.destroy());
    if (!run.current() || !editor.isConnected) release();
  } catch (error) {
    run.dispose();
    if (error?.name !== 'AbortError') throw error;
  }
}
