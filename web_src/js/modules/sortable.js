function retiredSortableError() {
  return new DOMException('Sortable initialization was retired', 'AbortError');
}

export async function createSortable(el, opts = {}, lifetime = {}) {
  const current = () => !lifetime.signal?.aborted && (lifetime.current?.() ?? true);
  if (!current()) throw retiredSortableError();
  const {Sortable} = await import(/* webpackChunkName: "sortablejs" */'sortablejs').catch((error) => {
    if (!current()) throw retiredSortableError();
    throw error;
  });
  if (!current()) throw retiredSortableError();
  const sortable = new Sortable(el, {
    animation: 150,
    ghostClass: 'card-ghost',
    onChoose: (e) => {
      const handle = opts.handle ? e.item.querySelector(opts.handle) : e.item;
      handle.classList.add('tw-cursor-grabbing');
      opts.onChoose?.(e);
    },
    onUnchoose: (e) => {
      const handle = opts.handle ? e.item.querySelector(opts.handle) : e.item;
      handle.classList.remove('tw-cursor-grabbing');
      opts.onUnchoose?.(e);
    },
    ...opts,
  });
  if (!current()) {
    const error = retiredSortableError();
    try { sortable.destroy() } catch (cause) { error.cause = cause }
    throw error;
  }
  return sortable;
}
