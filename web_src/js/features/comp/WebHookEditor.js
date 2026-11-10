import {POST} from '../../modules/fetch.js';
import {toggleElem} from '../../utils/dom.js';
import {contributionRootSelector, createPageFeatureRun} from '../../native-lifecycle.js';

// Element-bound listeners owned by the current page; retire removes exactly
// what init added so repeated entry never stacks handlers.
let httpMethodInput = null;
let testDeliveryEl = null;
let editorRun = null;

function onHttpMethodChange() {
  const visible = httpMethodInput.value === 'POST';
  toggleElem(document.getElementById('content_type').closest('.field'), visible);
}

async function onTestDeliveryClick() {
  const run = editorRun;
  if (!run?.current() || this.classList.contains('disabled')) return;
  const redirect = this.getAttribute('data-redirect');
  this.classList.add('is-loading', 'disabled');
  try {
    await POST(this.getAttribute('data-link'));
    if (!run.current()) return;
    await new Promise((resolve) => {
      const timer = setTimeout(resolve, 5000);
      run.own(() => {
        clearTimeout(timer);
        resolve();
      });
    });
    if (run.current()) window.location.href = redirect;
  } catch (error) {
    if (run.current()) console.error('Error sending webhook test delivery:', error);
  } finally {
    if (run.current()) this.classList.remove('is-loading');
  }
}

export function retireCompWebHookEditor() {
  editorRun?.dispose();
  editorRun = null;
  if (httpMethodInput) {
    httpMethodInput.removeEventListener('change', onHttpMethodChange);
    httpMethodInput = null;
  }
  if (testDeliveryEl) {
    testDeliveryEl.removeEventListener('click', onTestDeliveryClick);
    testDeliveryEl = null;
  }
}

export function initCompWebHookEditor(run = createPageFeatureRun()) {
  const root = document.querySelector('.new.webhook');
  if (!root || root.closest(contributionRootSelector)) {
    return;
  }
  retireCompWebHookEditor();
  editorRun = run;

  // some webhooks (like Gitea) allow to set the request method (GET/POST), and it would toggle the "Content Type" field
  httpMethodInput = document.getElementById('http_method');
  if (httpMethodInput) {
    onHttpMethodChange();
    httpMethodInput.addEventListener('change', onHttpMethodChange);
  }

  // Test delivery
  testDeliveryEl = document.getElementById('test-delivery');
  testDeliveryEl?.addEventListener('click', onTestDeliveryClick);
}
