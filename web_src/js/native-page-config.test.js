import {normalizeMentionValues, setPageConfig} from './native-page-config.js';

const savedPageData = window.config.pageData;
const savedMentionValues = window.config.mentionValues;

afterEach(() => {
  window.config.pageData = savedPageData;
  window.config.mentionValues = savedMentionValues;
});

test('mention normalization keeps first-name order and the last value without mutating input or config', () => {
  const oldReader = Object.freeze({name: 'reader', value: 'outdated'});
  const currentReader = Object.freeze({name: 'reader', value: 'current'});
  const editor = Object.freeze({name: 'editor', value: 'editor'});
  const organization = Object.freeze({name: 'organization', value: 'organization'});
  const values = Object.freeze([oldReader, editor, currentReader, organization]);

  const normalized = normalizeMentionValues(values);

  expect(normalized).toEqual([currentReader, editor, organization]);
  expect(normalized[0]).toBe(currentReader);
  expect(normalized).not.toBe(values);
  expect(values).toEqual([oldReader, editor, currentReader, organization]);
  expect(window.config.pageData).toBe(savedPageData);
  expect(window.config.mentionValues).toBe(savedMentionValues);
});

test('missing or empty mention input normalizes to an empty replacement', () => {
  expect(normalizeMentionValues()).toEqual([]);
  expect(normalizeMentionValues(null)).toEqual([]);
  expect(normalizeMentionValues([])).toEqual([]);
});

test('page config synchronously replaces mentions and clears stale per-page values', () => {
  const previous = {marker: 'old', mentionValues: [{name: 'reader', value: 'old'}]};
  setPageConfig(previous);
  expect(window.config.mentionValues).toEqual([{name: 'reader', value: 'old'}]);
  const incoming = {mentionValues: [{name: 'reader', value: 'first'}, {name: 'reader', value: 'last'}]};

  setPageConfig(incoming);
  expect(window.config.pageData).toBe(incoming);
  expect(window.config.pageData.marker).toBeUndefined();
  expect(window.config.mentionValues).toEqual([{name: 'reader', value: 'last'}]);
  setPageConfig({});
  expect(window.config.mentionValues).toEqual([]);
  setPageConfig(null);
  expect(window.config.pageData).toEqual({});
  expect(window.config.mentionValues).toEqual([]);
  expect(previous).toEqual({marker: 'old', mentionValues: [{name: 'reader', value: 'old'}]});
});
