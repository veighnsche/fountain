import {vi} from 'vitest';
import {createFeatureRun} from '../native-feature-run.js';
import {createDropzone} from './dropzone.js';
import {createSortable} from '../modules/sortable.js';

const widgets = vi.hoisted(() => ({instances: [], onCreate: null}));
vi.mock('dropzone', () => ({Dropzone: class {
  constructor(_el, options) {
    this.options = options;
    this.destroy = vi.fn();
    widgets.instances.push(this);
    widgets.onCreate?.();
  }
}}));
vi.mock('sortablejs', () => ({Sortable: class {
  constructor(_el, options) {
    this.options = options;
    this.destroy = vi.fn();
    widgets.instances.push(this);
    widgets.onCreate?.();
  }
}}));

beforeEach(() => {
  widgets.instances = [];
  widgets.onCreate = null;
});

test.each([['Dropzone', createDropzone], ['Sortable', createSortable]])('%s refuses its retired import before vendor construction', async (_name, create) => {
  const run = createFeatureRun();
  const pending = create(document.createElement('div'), {}, run);
  run.dispose();
  await expect(pending).rejects.toMatchObject({name: 'AbortError'});
  expect(widgets.instances).toHaveLength(0);
});

test.each([['Dropzone', createDropzone], ['Sortable', createSortable]])('%s destroys a constructor result if its originating run retires during construction', async (_name, create) => {
  const run = createFeatureRun();
  widgets.onCreate = () => run.dispose();
  await expect(create(document.createElement('div'), {}, run)).rejects.toMatchObject({name: 'AbortError'});
  expect(widgets.instances).toHaveLength(1);
  expect(widgets.instances[0].destroy).toHaveBeenCalledOnce();
});

test.each([['Dropzone', createDropzone], ['Sortable', createSortable]])('%s leaves a successful instance under caller ownership', async (_name, create) => {
  const run = createFeatureRun();
  const widget = await create(document.createElement('div'), {customOption: true}, run);
  expect(widget.options.customOption).toBe(true);
  expect(widget.options.signal).toBeUndefined();
  expect(widget.options.current).toBeUndefined();
  run.dispose();
  expect(widget.destroy).not.toHaveBeenCalled();
  widget.destroy();
  expect(widget.destroy).toHaveBeenCalledOnce();
});
