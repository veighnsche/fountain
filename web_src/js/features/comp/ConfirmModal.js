import $ from 'jquery';
import {svg} from '../../svg.js';
import {htmlEscape} from 'escape-goat';
import {createFeatureRun} from '../../native-feature-run.js';

const {i18n} = window.config;

export async function confirmModal(opts = {content: '', buttonColor: 'primary'}, run = createFeatureRun()) {
  if (!run.current()) return false;
  return new Promise((resolve) => {
    const $modal = $(`
<div class="ui g-modal-confirm modal">
  <div class="content">${htmlEscape(opts.content)}</div>
  <div class="actions">
    <button class="ui cancel button">${svg('octicon-x')} ${i18n.modal_cancel}</button>
    <button class="ui ${opts.buttonColor || 'primary'} ok button">${svg('octicon-check')} ${i18n.modal_confirm}</button>
  </div>
</div>
`);

    const release = run.own(() => {
      try {
        $modal.modal('hide').modal('destroy');
      } finally {
        $modal.remove();
        resolve(false);
      }
    });
    if (!run.current()) return;
    $modal.appendTo(document.body);
    $modal.modal({
      onApprove() {
        if (run.current()) resolve(true);
      },
      onHidden() {
        release();
      },
    }).modal('show');
  });
}
