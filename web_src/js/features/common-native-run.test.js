import $ from 'jquery';
import {vi} from 'vitest';
import {GET, POST, request} from '../modules/fetch.js';
import {createPageFeatureRun, retirePageFeatures} from '../native-lifecycle.js';
import {initDropzone, initGlobalCommonListeners, initGlobalLinkActions, initGlobalShowModal, retireNativeNodes} from './common-global.js';
import {createDropzone} from './dropzone.js';

vi.mock('../vendor/jquery.are-you-sure.js', () => ({}));
vi.mock('../htmx.js', () => ({}));
vi.mock('../modules/fetch.js', () => ({GET: vi.fn(), POST: vi.fn(), request: vi.fn()}));
vi.mock('./dropzone.js', () => ({createDropzone: vi.fn()}));

function deferred() {
  let resolve;
  const promise = new Promise((done) => { resolve = done });
  return {promise, resolve};
}

function installDropzone() {
  document.body.innerHTML = `<div class="page-content">
    <div id="attachment-zone" data-attachment-url="/attachments/list">
      <div id="dropzone" class="dropzone" data-remove-url="/attachments/remove"><div class="files"></div></div>
    </div>
  </div>`;
  return {element: document.querySelector('#dropzone'), zone: document.querySelector('#attachment-zone')};
}

beforeEach(() => {
  createDropzone.mockReset();
  GET.mockReset();
  POST.mockReset();
  request.mockReset();
});

afterEach(() => {
  retireNativeNodes();
  retirePageFeatures();
  document.body.innerHTML = '';
  vi.restoreAllMocks();
});

test('late Dropzone construction destroys only its originating instance across a same-ID page replacement', async () => {
  const oldConstruction = deferred();
  const newConstruction = deferred();
  const oldWidget = {destroy: vi.fn()};
  const newWidget = {destroy: vi.fn()};
  createDropzone.mockReturnValueOnce(oldConstruction.promise).mockReturnValueOnce(newConstruction.promise);

  const oldPage = installDropzone();
  const oldMount = initDropzone(oldPage.element, oldPage.zone);
  retireNativeNodes();
  retirePageFeatures();
  const newPage = installDropzone();
  const newMount = initDropzone(newPage.element, newPage.zone);
  expect(createDropzone).toHaveBeenCalledTimes(2);

  oldConstruction.resolve(oldWidget);
  await oldMount;
  expect(oldWidget.destroy).toHaveBeenCalledTimes(1);
  expect(newWidget.destroy).not.toHaveBeenCalled();
  expect(initDropzone(newPage.element, newPage.zone)).toBe(newMount);

  newConstruction.resolve(newWidget);
  await newMount;
  expect(newWidget.destroy).not.toHaveBeenCalled();
  retireNativeNodes();
  retireNativeNodes();
  expect(oldWidget.destroy).toHaveBeenCalledTimes(1);
  expect(newWidget.destroy).toHaveBeenCalledTimes(1);
});

test('a failed Dropzone does not revoke its caller-owned repository run and can be retried', async () => {
  const run = createPageFeatureRun();
  const page = installDropzone();
  const failure = new Error('Could not load attachment widget');
  createDropzone.mockRejectedValueOnce(failure);
  await expect(initDropzone(page.element, page.zone, run)).rejects.toBe(failure);
  expect(run.current()).toBe(true);
  expect(run.signal.aborted).toBe(false);

  const widget = {destroy: vi.fn()};
  createDropzone.mockResolvedValueOnce(widget);
  await initDropzone(page.element, page.zone, run);
  run.dispose();
  expect(widget.destroy).toHaveBeenCalledTimes(1);
});

test('retiring the page closes a delegated link confirmation without approving its mutation', async () => {
  document.body.innerHTML = '<div class="page-content"><button class="link-action" data-url="/delete" data-modal-confirm="Delete?"></button></div>';
  let options;
  const modal = vi.fn(function (operation) {
    if (typeof operation === 'object') options = operation;
    if (operation === 'hide') options.onHidden();
    return this;
  });
  $.fn.modal = modal;
  try {
    initGlobalCommonListeners();
    document.querySelector('button').click();
    expect(document.querySelector('.g-modal-confirm')).not.toBeNull();
    retirePageFeatures();
    await Promise.resolve();
    expect(document.querySelector('.g-modal-confirm')).toBeNull();
    options.onApprove();
    await Promise.resolve();
    expect(request).not.toHaveBeenCalled();
    expect(POST).not.toHaveBeenCalled();
    expect(modal.mock.calls.filter(([operation]) => operation === 'destroy')).toHaveLength(1);
  } finally {
    delete $.fn.modal;
  }
});

test('a retired attachment reload body cannot touch replacement inputs or delete uploads during widget destruction', async () => {
  const body = deferred();
  const json = vi.fn(() => body.promise);
  GET.mockResolvedValue({json});
  const handlers = new Map();
  const widget = {
    on: vi.fn((event, callback) => handlers.set(event, callback)),
    emit: vi.fn((event, ...args) => handlers.get(event)?.(...args)),
    removeAllFiles: vi.fn(),
    destroy: vi.fn(),
  };
  createDropzone.mockImplementation(async (_element, options) => {
    options.init.call(widget);
    return widget;
  });
  const page = installDropzone();
  await initDropzone(page.element, page.zone);
  const uploaded = {name: 'draft.txt', type: 'text/plain', previewTemplate: document.createElement('div')};
  widget.emit('success', uploaded, {uuid: 'same-upload'});
  widget.destroy.mockImplementation(() => widget.emit('removedfile', uploaded));
  const reload = widget.emit('reload');
  await Promise.resolve();
  expect(json).toHaveBeenCalledTimes(1);

  retireNativeNodes();
  retirePageFeatures();
  installDropzone();
  const incomingFiles = document.querySelector('.files');
  incomingFiles.innerHTML = '<input id="same-upload" name="files" value="incoming"><input name="files_fullpath[same-upload]" value="incoming.txt">';
  const incomingHTML = incomingFiles.innerHTML;
  body.resolve([{uuid: 'stale-upload', name: 'old.txt', mime_type: 'text/plain'}]);
  await reload;

  expect(widget.destroy).toHaveBeenCalledTimes(1);
  expect(widget.removeAllFiles).not.toHaveBeenCalled();
  expect(widget.emit.mock.calls.some(([event]) => event === 'addedfile')).toBe(false);
  expect(incomingFiles.innerHTML).toBe(incomingHTML);
  expect(POST).not.toHaveBeenCalled();
});

test('native delegated handlers leave contribution delete, modal and HTMX confirmations to their owner', () => {
  document.body.innerHTML = `<div class="page-content">
    <button id="native-request">request</button>
    <div data-extension-panel>
      <button id="contribution-delete" class="delete-button" data-modal-id="owned-dialog">delete</button>
      <button id="contribution-modal" class="show-modal" data-modal="#owned-dialog">show</button>
      <button id="contribution-request">request</button>
      <div id="owned-dialog" class="ui modal"><span class="name">Owner content</span></div>
    </div>
  </div>`;
  const modal = vi.fn(function () { return this });
  $.fn.modal = modal;
  try {
    initGlobalLinkActions();
    initGlobalLinkActions();
    initGlobalShowModal();
    initGlobalShowModal();
    for (const id of ['contribution-delete', 'contribution-modal']) {
      const click = new MouseEvent('click', {bubbles: true, cancelable: true});
      document.getElementById(id).dispatchEvent(click);
      expect(click.defaultPrevented).toBe(false);
    }
    for (const question of ['', 'Owner confirmation?']) {
      const issueRequest = vi.fn();
      const element = document.querySelector('#contribution-request');
      const event = new CustomEvent('htmx:confirm', {bubbles: true, cancelable: true, detail: {elt: element, question, issueRequest}});
      element.dispatchEvent(event);
      expect(event.defaultPrevented).toBe(false);
      expect(issueRequest).not.toHaveBeenCalled();
    }
    const issueRequest = vi.fn();
    const element = document.querySelector('#native-request');
    const nativeEvent = new CustomEvent('htmx:confirm', {bubbles: true, cancelable: true, detail: {elt: element, question: '', issueRequest}});
    element.dispatchEvent(nativeEvent);
    expect(nativeEvent.defaultPrevented).toBe(true);
    expect(issueRequest).toHaveBeenCalledExactlyOnceWith(true);
    expect(modal).not.toHaveBeenCalled();
    expect(document.querySelector('#owned-dialog .name').textContent).toBe('Owner content');
    expect(POST).not.toHaveBeenCalled();
    expect(request).not.toHaveBeenCalled();
  } finally {
    delete $.fn.modal;
  }
});
