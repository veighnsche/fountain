import {vi} from 'vitest';
import {retireNativeModals, showModal} from './modal.ts';

function installModal(parent, id) {
  const modal = document.createElement('dialog');
  modal.id = id;
  modal.innerHTML = '<button class="ok">OK</button><button class="cancel">Cancel</button>';
  modal.showModal = vi.fn(() => modal.setAttribute('open', ''));
  modal.close = vi.fn(() => modal.removeAttribute('open'));
  parent.append(modal);
  return modal;
}

afterEach(() => {
  retireNativeModals();
  document.body.replaceChildren();
});

test('reopened native dialogs preserve their original position and release handlers on retirement', () => {
  const page = document.createElement('main');
  page.className = 'page-content';
  document.body.append(page);
  const before = document.createElement('span');
  page.append(before);
  const modal = installModal(page, 'native-dialog');
  const after = document.createElement('span');
  page.append(after);
  const previous = vi.fn();
  const latest = vi.fn();
  showModal(modal.id, previous);
  modal.close();
  showModal(modal.id, latest);
  expect(modal.parentElement).toBe(document.body);
  retireNativeModals();
  expect(modal.open).toBe(false);
  expect(Array.from(page.children)).toEqual([before, modal, after]);
  modal.querySelector('.ok').click();
  modal.querySelector('.cancel').click();
  expect(previous).not.toHaveBeenCalled();
  expect(latest).not.toHaveBeenCalled();
  expect(modal.close).toHaveBeenCalledTimes(2);
  retireNativeModals();
  expect(modal.close).toHaveBeenCalledTimes(2);
});

test('an orphaned native dialog is removed while global and contribution dialogs keep their owners', () => {
  const page = document.createElement('main');
  page.className = 'page-content';
  const contribution = document.createElement('div');
  contribution.setAttribute('data-extension-page', '');
  page.append(contribution);
  document.body.append(page);
  const native = installModal(page, 'native-dialog');
  const global = installModal(document.body, 'global-dialog');
  const owned = installModal(contribution, 'owned-dialog');
  const globalApprove = vi.fn();
  const ownedApprove = vi.fn();
  showModal(native.id, vi.fn());
  showModal(global.id, globalApprove);
  showModal(owned.id, ownedApprove);
  page.remove();
  retireNativeModals();
  expect(native.isConnected).toBe(false);
  expect(global.open).toBe(true);
  expect(owned.open).toBe(true);
  global.querySelector('.ok').click();
  owned.querySelector('.ok').click();
  expect(globalApprove).toHaveBeenCalledOnce();
  expect(ownedApprove).toHaveBeenCalledOnce();
});
