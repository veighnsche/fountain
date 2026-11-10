// Copyright 2025 The Forgejo Authors. All rights reserved.
// SPDX-License-Identifier: MIT

import {contributionRootSelector, createPageFeatureRun} from '../native-lifecycle.js';

// The currently wired approve/cancel handlers per modal element. Re-showing
// a modal replaces them instead of stacking another action on its buttons.
const modalHandlers = new WeakMap<HTMLElement, {ok?: () => void; cancel: () => void}>();
const modalOrigins = new WeakMap<HTMLElement, boolean>();
const nativeModals = new Map<HTMLElement, {placeholder: Comment; cleanup: () => void; run: ReturnType<typeof createPageFeatureRun>}>();

export function getNativeModalRun(element: Element) {
  const modal = element.closest<HTMLElement>('dialog,.ui.modal');
  return modal ? nativeModals.get(modal)?.run : undefined;
}

export function trackNativeModal(modal: HTMLElement, cleanup: () => void): () => boolean {
  let native = modalOrigins.get(modal);
  if (native === undefined) {
    native = Boolean(modal.closest('.page-content') && !modal.closest(contributionRootSelector));
    modalOrigins.set(modal, native);
  }
  if (!native) return () => true;
  let state = nativeModals.get(modal);
  if (!state) {
    const placeholder = document.createComment('native modal');
    modal.before(placeholder);
    state = {placeholder, cleanup, run: createPageFeatureRun()};
    const owned = state;
    owned.run.own(() => owned.cleanup());
    nativeModals.set(modal, owned);
  }
  state.cleanup = cleanup;
  return state.run.current;
}

export function retireNativeModals() {
  let firstError;
  for (const [modal, state] of Array.from(nativeModals)) {
    try {
      state.run.dispose();
    } catch (error) {
      firstError ??= error;
    } finally {
      nativeModals.delete(modal);
      if (state.placeholder.isConnected) state.placeholder.replaceWith(modal);
      else modal.remove();
      state.placeholder.remove();
    }
  }
  if (firstError) throw firstError;
}

function unbindModal(modal: HTMLDialogElement) {
  const previous = modalHandlers.get(modal);
  if (previous?.ok) modal.querySelector('.ok')?.removeEventListener('click', previous.ok);
  if (previous) modal.querySelector('.cancel')?.removeEventListener('click', previous.cancel);
  modalHandlers.delete(modal);
}

// showModal will show the given modal and run `onApprove` if the approve/ok/yes
// button is pressed.
export function showModal(modalID: string, onApprove: () => void) {
  const modal = document.getElementById(modalID) as HTMLDialogElement;
  const current = trackNativeModal(modal, () => {
    if (modal.open) modal.close();
    unbindModal(modal);
  });
  if (!current()) return;
  // Move the modal to `<body>`, to avoid inheriting any bad CSS or if the
  // parent becomes `display: hidden`.
  document.body.append(modal);

  const okButton = modal.querySelector('.ok');
  const cancelButton = modal.querySelector('.cancel');
  unbindModal(modal);
  const handlers: {ok?: () => void; cancel: () => void} = {
    cancel: () => {
      modal.close();
    },
  };
  if (onApprove) handlers.ok = () => { if (current()) onApprove(); };
  modalHandlers.set(modal, handlers);

  // Close the modal if the cancel button is pressed.
  cancelButton?.addEventListener('click', handlers.cancel, {passive: true});
  if (handlers.ok) okButton?.addEventListener('click', handlers.ok, {passive: true});

  // The modal is ready to be shown.
  modal.showModal();
}

let modalCloseBound = false;

// NOTE: Can be replaced in late 2026 with `closedBy` attribute on `<dialog>` element.
export function initModalClose() {
  if (modalCloseBound) return;
  modalCloseBound = true;
  document.addEventListener('click', (event) => {
    const dialog = document.querySelector<HTMLDialogElement>('dialog[open]');
    // No open dialogs on page, nothing to do.
    if (dialog === null) return;

    const target = event.target as HTMLElement;
    // User clicked dialog itself (not it's content), likely ::backdrop, so close it.
    if (dialog === target) dialog.close();
  });
}
