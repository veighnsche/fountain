import $ from 'jquery';
import {vi} from 'vitest';
import {confirmModal} from './ConfirmModal.js';
import {createFeatureRun} from '../../native-feature-run.js';

let modal;
const options = new Map();

beforeEach(() => {
  options.clear();
  modal = vi.fn(function (option) {
    for (const element of this) {
      if (typeof option === 'object') options.set(element, option);
      if (option === 'hide') options.get(element)?.onHidden();
    }
    return this;
  });
  $.fn.modal = modal;
});

afterEach(() => {
  document.body.innerHTML = '';
  delete $.fn.modal;
});

test('retiring a native confirmation destroys only its own modal and resolves its pending decision false', async () => {
  const run = createFeatureRun();
  const nativeDecision = confirmModal({content: 'Delete native item?'}, run);
  const nativeModal = document.querySelector('.g-modal-confirm');
  const globalDecision = confirmModal({content: 'Global confirmation'});
  const globalModal = document.querySelectorAll('.g-modal-confirm')[1];
  const nativeOptions = options.get(nativeModal);

  run.dispose();
  run.dispose();
  expect(await nativeDecision).toBe(false);
  expect(nativeModal.isConnected).toBe(false);
  expect(globalModal.isConnected).toBe(true);
  nativeOptions.onApprove();
  expect(modal.mock.calls.filter(([operation]) => operation === 'destroy')).toHaveLength(1);

  options.get(globalModal).onHidden();
  expect(await globalDecision).toBe(false);
  expect(globalModal.isConnected).toBe(false);
});

test('an approved current confirmation preserves its true result and releases its widget once hidden', async () => {
  const run = createFeatureRun();
  const decision = confirmModal({content: 'Proceed?'}, run);
  const element = document.querySelector('.g-modal-confirm');
  options.get(element).onApprove();
  expect(await decision).toBe(true);
  options.get(element).onHidden();
  run.dispose();
  expect(element.isConnected).toBe(false);
  expect(modal.mock.calls.filter(([operation]) => operation === 'destroy')).toHaveLength(1);
});
