import {POST} from '../../modules/fetch.js';
import {initCompWebHookEditor, retireCompWebHookEditor} from './WebHookEditor.js';

vi.mock('../../modules/fetch.js', () => ({POST: vi.fn()}));

function installWebhook() {
  document.body.innerHTML = '<div class="new webhook"><button id="test-delivery" data-link="/webhook/test" data-redirect="/webhook/deliveries">test</button></div>';
  initCompWebHookEditor();
  return document.getElementById('test-delivery');
}

beforeEach(() => {
  vi.useFakeTimers();
  window.history.replaceState(null, '', '/webhook');
});

afterEach(async () => {
  retireCompWebHookEditor();
  await Promise.resolve();
  document.body.innerHTML = '';
  window.history.replaceState(null, '', '/');
  vi.useRealTimers();
  vi.restoreAllMocks();
});

test('a current webhook delivery sends one POST and redirects after the existing five-second delay', async () => {
  POST.mockResolvedValue(undefined);
  const button = installWebhook();
  button.click();
  button.click();
  await Promise.resolve();
  button.dataset.redirect = '/different-page';

  expect(POST).toHaveBeenCalledExactlyOnceWith('/webhook/test');
  await vi.advanceTimersByTimeAsync(4999);
  expect(window.location.pathname).toBe('/webhook');
  await vi.advanceTimersByTimeAsync(1);
  expect(window.location.pathname).toBe('/webhook/deliveries');
});

test('a retired delivery POST cannot acquire a redirect timer or clear incoming loading state', async () => {
  const pending = Promise.withResolvers();
  POST.mockReturnValue(pending.promise);
  const oldButton = installWebhook();
  oldButton.click();
  retireCompWebHookEditor();
  const incoming = installWebhook();
  incoming.classList.add('is-loading');
  window.history.replaceState(null, '', '/incoming');

  pending.resolve();
  await Promise.resolve();
  await vi.advanceTimersByTimeAsync(5000);

  expect(window.location.pathname).toBe('/incoming');
  expect(vi.getTimerCount()).toBe(0);
  expect(oldButton.classList.contains('is-loading')).toBe(true);
  expect(incoming.classList.contains('is-loading')).toBe(true);
  expect(POST).toHaveBeenCalledExactlyOnceWith('/webhook/test');
});

test('retirement cancels an already scheduled redirect without replaying the delivery', async () => {
  POST.mockResolvedValue(undefined);
  installWebhook().click();
  await Promise.resolve();
  expect(vi.getTimerCount()).toBe(1);

  retireCompWebHookEditor();
  window.history.replaceState(null, '', '/incoming');
  await vi.advanceTimersByTimeAsync(5000);

  expect(window.location.pathname).toBe('/incoming');
  expect(vi.getTimerCount()).toBe(0);
  expect(POST).toHaveBeenCalledExactlyOnceWith('/webhook/test');
});

test('a current delivery error is observed and never automatically retries the mutation', async () => {
  const error = new Error('delivery failed');
  POST.mockRejectedValue(error);
  const report = vi.spyOn(console, 'error').mockImplementation(() => {});
  const button = installWebhook();
  button.click();
  await Promise.resolve();

  expect(report).toHaveBeenCalledExactlyOnceWith('Error sending webhook test delivery:', error);
  expect(button.classList.contains('is-loading')).toBe(false);
  expect(button.classList.contains('disabled')).toBe(true);
  button.click();
  await vi.advanceTimersByTimeAsync(5000);
  expect(POST).toHaveBeenCalledExactlyOnceWith('/webhook/test');
  expect(window.location.pathname).toBe('/webhook');
});

test('a stale delivery error does not report or clear state on a later page', async () => {
  const pending = Promise.withResolvers();
  POST.mockReturnValue(pending.promise);
  const report = vi.spyOn(console, 'error').mockImplementation(() => {});
  installWebhook().click();
  retireCompWebHookEditor();
  const incoming = installWebhook();
  incoming.classList.add('is-loading');

  pending.reject(new Error('outgoing failure'));
  await Promise.resolve();

  expect(report).not.toHaveBeenCalled();
  expect(incoming.classList.contains('is-loading')).toBe(true);
  expect(vi.getTimerCount()).toBe(0);
});
