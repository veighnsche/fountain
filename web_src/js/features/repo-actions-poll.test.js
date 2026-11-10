import {readFileSync} from 'node:fs';
import {initRepoActionsList} from './repo-actions-list.js';

function beforeRequest(source, trigger) {
  const event = new CustomEvent('htmx:beforeRequest', {
    bubbles: true,
    cancelable: true,
    detail: {elt: source, requestConfig: {triggeringEvent: trigger ? new Event(trigger) : undefined}},
  });
  source.dispatchEvent(event);
  return event.defaultPrevented;
}

beforeAll(() => initRepoActionsList());

beforeEach(() => {
  Object.defineProperty(document, 'visibilityState', {value: 'visible', configurable: true});
  document.body.innerHTML = '<div data-native-actions-poll id="poll"><button id="manual">dispatch</button></div><div id="unmarked"></div>';
});

afterEach(() => {
  document.body.innerHTML = '';
});

test('the actions template declares the polling source and keeps both existing polling triggers without globals', () => {
  const template = readFileSync('templates/repo/actions/list.tmpl', 'utf8');
  const native = new DOMParser().parseFromString(template, 'text/html');
  const poll = native.querySelector('[data-native-actions-poll]');

  expect(poll.getAttribute('hx-trigger')).toBe('every 30s, visibilitychange from:document, simulate-polling-interval from:document');
  expect(poll.getAttribute('hx-indicator')).toBe('#reloading-indicator');
  expect(template).not.toContain('pollingOk()');
});

test('interval and simulated polling use the same visibility and expanded-menu predicate', () => {
  const poll = document.getElementById('poll');
  for (const trigger of [undefined, 'simulate-polling-interval']) expect(beforeRequest(poll, trigger)).toBe(false);
  document.body.insertAdjacentHTML('beforeend', '<button aria-expanded="true">expanded</button>');
  for (const trigger of [undefined, 'simulate-polling-interval']) expect(beforeRequest(poll, trigger)).toBe(true);
  document.querySelector('[aria-expanded]').remove();
  Object.defineProperty(document, 'visibilityState', {value: 'hidden', configurable: true});
  for (const trigger of [undefined, 'simulate-polling-interval']) expect(beforeRequest(poll, trigger)).toBe(true);
});

test('a visible branch form blocks interval polling while a hidden branch form permits it', () => {
  const poll = document.getElementById('poll');
  const form = document.createElement('form');
  form.id = 'branch-dropdown-form';
  form.checkVisibility = () => true;
  document.body.append(form);
  expect(beforeRequest(poll)).toBe(true);
  expect(beforeRequest(poll, 'simulate-polling-interval')).toBe(true);
  form.checkVisibility = () => false;
  expect(beforeRequest(poll)).toBe(false);
  expect(beforeRequest(poll, 'simulate-polling-interval')).toBe(false);
});

test('visibility refresh still requires only visible document even while a dropdown is open', () => {
  const poll = document.getElementById('poll');
  document.body.insertAdjacentHTML('beforeend', '<button aria-expanded="true">expanded</button>');

  expect(beforeRequest(poll, 'visibilitychange')).toBe(false);
  Object.defineProperty(document, 'visibilityState', {value: 'hidden', configurable: true});
  expect(beforeRequest(poll, 'visibilitychange')).toBe(true);
});

test('the gate affects only the marked native request source and leaves child manual actions alone', () => {
  Object.defineProperty(document, 'visibilityState', {value: 'hidden', configurable: true});
  document.body.insertAdjacentHTML('beforeend', '<div data-extension-panel><div data-native-actions-poll id="owned"></div></div>');

  expect(beforeRequest(document.getElementById('poll'))).toBe(true);
  expect(beforeRequest(document.getElementById('manual'))).toBe(false);
  expect(beforeRequest(document.getElementById('unmarked'))).toBe(false);
  expect(beforeRequest(document.getElementById('owned'))).toBe(false);
});
