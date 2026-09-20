const errors = [
  '', '', 'InvalidData', 'Unsupported', 'LimitExceeded', 'Cancelled',
  'OutOfMemory', 'InvalidArgument', 'Busy', 'MissingComponent',
];
const errorCodes = new Set([...errors.slice(2), 'ComponentLoadFailed', 'SourceReadFailed']);
const scopes = { page: 0, includes: 1, thumbnail: 2 };
let core;
let active;
let lastPage;
let source;
let opening;
let metadataActive;
let cacheLimit = 0;
let requestSequence = 0;
let renderTiming = { steps: 0, maxStepMs: 0 };
const operations = new Set();
const loads = new Map();
const requests = new Map();
const channel = new MessageChannel();
const turns = [];
channel.port1.onmessage = () => turns.shift()?.();
const yieldTask = () => new Promise(resolve => {
  turns.push(resolve);
  channel.port2.postMessage(0);
});
const reply = (id, result) => postMessage({ id, result });
const fail = (id, error) => postMessage({ id, error: {
  code: errorCodes.has(error?.code ?? error?.message) ? (error.code ?? error.message) : 'WorkerFailed',
  message: error?.message ?? String(error),
} });

function check(status) {
  if (status === 0) return;
  const code = errors[status] ?? 'WorkerFailed';
  // Copy while the failed call is current: cleanup or another request can
  // overwrite the core diagnostic before this error reaches the host.
  const message = new TextDecoder().decode(new Uint8Array(
    core.memory.buffer, core.error_message_ptr(), core.error_message_len(),
  ));
  throw Object.assign(new Error(message || code), { code });
}

function releaseJob() {
  core.render_cancel();
  lastPage = undefined;
}

function stop() {
  if (active) {
    active.controller.abort();
    releaseJob();
    fail(active.id, new Error('Cancelled'));
    active = null;
  }
}

function begin(id) {
  const token = { id, controller: new AbortController() };
  operations.add(token);
  return token;
}

function stopMetadata() {
  if (!metadataActive) return;
  metadataActive.controller.abort();
  metadataActive = null;
  core.metadata_release();
}

function invalidate() {
  stop();
  stopMetadata();
  for (const token of operations) token.controller.abort();
  lastPage = undefined;
}

function component(index) {
  const ptr = core.component_info(index);
  if (!ptr) check(core.last_status());
  const view = new DataView(core.memory.buffer, ptr, 44);
  const string = offset => {
    try {
      const bytes = new Uint8Array(
        core.memory.buffer, view.getUint32(offset, true), view.getUint32(offset + 4, true),
      );
      return new TextDecoder('utf-8', { fatal: true, ignoreBOM: true }).decode(bytes);
    } catch {
      throw new Error('InvalidData');
    }
  };
  return {
    index, id: string(0), name: string(8), title: string(16),
    kind: ['shared', 'page', 'thumbnail', 'shared-annotations'][view.getUint32(24, true)],
    size: view.getUint32(28, true), loaded: !!view.getUint32(32, true),
    range: view.getUint32(40, true)
      ? { offset: view.getUint32(36, true), length: view.getUint32(40, true) }
      : null,
  };
}

function settle(load, error) {
  requests.delete(load.request);
  if (loads.get(load.key) === load) loads.delete(load.key);
  load.done = true;
  if (error) load.reject(error); else load.resolve();
}

function response(message) {
  const load = requests.get(message.request);
  if (!load || message.source !== source) return;
  try {
    if (message.error) throw new Error(message.error);
    if (!(message.bytes instanceof ArrayBuffer)) throw new Error('InvalidArgument');
    if (!message.bytes.byteLength || message.bytes.byteLength > 128 * 1024 * 1024) throw new Error('LimitExceeded');
    const whole = load.metadata && message.whole;
    if (load.range && !whole && message.bytes.byteLength !== load.range.length) throw new Error('InvalidArgument');
    if (load.index !== null && (!load.range || whole) && component(load.index).loaded) { settle(load); return; }
    const ptr = load.metadata && !whole ? core.metadata_alloc()
      : load.range && !whole ? core.source_alloc() : core.component_alloc(load.index, message.bytes.byteLength);
    if (!ptr) check(core.last_status());
    new Uint8Array(core.memory.buffer, ptr, message.bytes.byteLength).set(new Uint8Array(message.bytes));
    check(load.metadata && !whole ? core.metadata_commit(integer(message.size, 1, 0xffffffff))
      : load.range && !whole ? core.source_commit() : core.component_commit(load.index));
    settle(load);
  } catch (error) {
    core.component_abort();
    settle(load, error);
  }
}

async function loadBytes(index, range, token, metadata = false) {
  const signal = token.controller.signal;
  if (signal.aborted) throw new Error('Cancelled');
  const key = metadata ? `metadata:${source}:${index}:${range.offset}`
    : range ? `source:${source}:${range.offset}` : index;
  let load = loads.get(key);
  if (!load) {
    const request = metadata
      ? { type: 'metadata-request', range, component: index === null ? null : component(index) }
      : range
      ? { type: 'range-request', range }
      : { type: 'component-request', component: component(index) };
    load = { key, index, range, metadata, request: ++requestSequence, users: 0, done: false };
    load.promise = new Promise((resolve, reject) => {
      load.resolve = resolve;
      load.reject = reject;
    });
    loads.set(key, load);
    requests.set(load.request, load);
    postMessage({ ...request, request: load.request, source });
  }
  load.users++;
  let abort;
  try {
    await new Promise((resolve, reject) => {
      abort = () => reject(new Error('Cancelled'));
      signal.addEventListener('abort', abort, { once: true });
      load.promise.then(resolve, reject);
    });
    if (signal.aborted) throw new Error('Cancelled');
  } finally {
    signal.removeEventListener('abort', abort);
    if (--load.users === 0 && !load.done) {
      postMessage({ type: 'component-cancel', request: load.request });
      settle(load, new Error('Cancelled'));
    }
  }
}

async function prepare(page, scope, token) {
  while (!token.controller.signal.aborted) {
    const missing = core.next_missing(page, scope);
    check(core.last_status());
    if (!missing) return;
    await loadBytes(missing - 1, null, token);
  }
  throw new Error('Cancelled');
}

async function read(message, page, scope, action) {
  trimCache(page);
  const token = begin(message.id);
  try {
    await prepare(page, scope, token);
    if (token.controller.signal.aborted) throw new Error('Cancelled');
    action();
  } finally { operations.delete(token); }
}

function trimCache(page) {
  if (page === lastPage || core.cache_bytes() <= cacheLimit) return;
  // Preparation and decoding borrow components across awaits. Evict only at
  // an idle boundary; aborted operations cannot resume using the old bytes.
  for (const token of operations) if (!token.controller.signal.aborted) return;
  releaseJob();
  check(core.trim_cache(cacheLimit));
}

function integer(value, min, max) {
  if (!Number.isInteger(value) || value < min || value > max) throw new Error('InvalidArgument');
  return value;
}

function regionArgs(region) {
  return region == null ? null : [
    integer(region.x, 0, 0xffffffff), integer(region.y, 0, 0xffffffff),
    integer(region.width, 1, 0xffffffff), integer(region.height, 1, 0xffffffff),
  ];
}

function options(message) {
  const page = integer(message.page, 0, core.page_count() - 1);
  const ss = integer(message.subsample, 1, 256);
  const rotation = integer(message.rotation, 0, 3);
  const region = regionArgs(message.region);
  const size = message.size == null ? null : [
    integer(message.size.width, 1, 0xffffffff),
    integer(message.size.height, 1, 0xffffffff),
  ];
  if (size && ss !== 1) throw new Error('InvalidArgument');
  return { page, ss, size, rotation, region };
}

function geometry({ page, ss, size, rotation, region }) {
  let ptr;
  if (size) {
    ptr = core.page_transform_sized(page, ...size, rotation, ...(region ?? [0, 0, 0, 0]));
  } else if (region) {
    ptr = core.page_transform_region(page, ss, rotation, ...region);
  } else {
    ptr = core.page_transform(page, ss, rotation);
  }
  if (!ptr) check(core.last_status());
  const data = new DataView(core.memory.buffer, ptr, 128);
  return {
    x: data.getUint32(0, true), y: data.getUint32(4, true),
    width: data.getUint32(8, true), height: data.getUint32(12, true),
    pageWidth: data.getUint32(16, true), pageHeight: data.getUint32(20, true), rotation: data.getUint32(24, true),
    matrix: Array.from({ length: 6 }, (_, i) => data.getFloat64(32 + i * 8, true)),
    inverse: Array.from({ length: 6 }, (_, i) => data.getFloat64(80 + i * 8, true)),
  };
}

function text(id, page) {
  try {
    check(core.text_load(page));
    if (!core.text_present()) {
      reply(id, null);
      return;
    }
    const bytes = new Uint8Array(core.memory.buffer, core.text_ptr(), core.text_len()).slice().buffer;
    const names = ['', 'page', 'column', 'region', 'paragraph', 'line', 'word', 'character'];
    const count = core.text_zones_count();
    const data = new DataView(core.memory.buffer, core.text_zones_ptr(), count * 36);
    const zones = Array.from({ length: count }, (_, i) => {
      const p = i * 36;
      const parent = data.getUint32(p + 4, true);
      return {
        type: names[data.getUint32(p, true)], parent: parent === 0xffffffff ? null : parent,
        x: data.getInt32(p + 8, true), y: data.getInt32(p + 12, true),
        width: data.getInt32(p + 16, true), height: data.getInt32(p + 20, true),
        start: data.getUint32(p + 24, true), length: data.getUint32(p + 28, true),
        subtreeEnd: data.getUint32(p + 32, true),
      };
    });
    const result = {
      text: new TextDecoder('utf-8', { ignoreBOM: true }).decode(bytes), bytes,
      hasReplacements: !!core.text_has_replacements(), zones,
    };
    postMessage({ id, result }, [bytes]);
  } finally { core.text_release(); }
}

function readJson(id, name, ...args) {
  try {
    check(core[`${name}_load`](...args));
    let result = null;
    if (core[`${name}_present`]()) {
      const bytes = new Uint8Array(core.memory.buffer, core[`${name}_ptr`](), core[`${name}_len`]());
      const json = new TextDecoder('utf-8', { fatal: true }).decode(bytes);
      result = JSON.parse(json);
    }
    if (name === 'annotations' && result) {
      result.bytes = (result.bytes === null
        ? new TextEncoder().encode(result.source)
        : Uint8Array.from(result.bytes)).buffer;
      postMessage({ id, result }, [result.bytes]);
    } else reply(id, result);
  } finally { core[`${name}_release`](); }
}

function resolveLink(message) {
  if (typeof message.href !== 'string') throw new Error('InvalidArgument');
  if (message.href.length > 128 * 1024 * 1024) throw new Error('LimitExceeded');
  const origin = message.fromPage == null ? 0xffffffff : integer(message.fromPage, 0, core.page_count() - 1);
  const bytes = new TextEncoder().encode(message.href);
  // TextEncoder replaces lone UTF-16 surrogates; a changed ID must not resolve
  // to some other page. Reject any string that changes during the roundtrip.
  if (new TextDecoder('utf-8', { ignoreBOM: true }).decode(bytes) !== message.href) throw new Error('InvalidArgument');
  try {
    const ptr = core.link_alloc(bytes.length);
    if (!ptr) check(core.last_status());
    new Uint8Array(core.memory.buffer, ptr, bytes.length).set(bytes);
    const result = core.link_resolve(origin);
    if (!result) check(core.last_status());
    const view = new DataView(core.memory.buffer, result, 8);
    const page = view.getUint32(4, true);
    reply(message.id, {
      kind: ['none', 'page', 'url', 'options', 'unresolved'][view.getUint32(0, true)],
      page: page === 0xffffffff ? null : page,
    });
  } finally { core.link_release(); }
}

function startRender({ page, ss, size, rotation, region }, thumbnail) {
  if (thumbnail) {
    lastPage = undefined;
    check(core.thumbnail_start(page));
    return !!core.thumbnail_present();
  }
  if (size) {
    const args = [...size, rotation, ...(region ?? [0, 0, 0, 0])];
    check(page === lastPage ? core.render_restart_sized(...args) : core.render_start_sized(page, ...args));
  } else if (page === lastPage) {
    check(region ? core.render_restart_region(ss, rotation, ...region) : core.render_restart(ss, rotation));
  } else {
    check(region ? core.render_start_region(page, ss, rotation, ...region) : core.render_start(page, ss, rotation));
  }
  return true;
}

function copyRaster(thumbnail) {
  // A view cannot outlive memory.grow or another render. Transfer a copy.
  const rgba = new Uint8Array(core.memory.buffer, core.result_ptr(), core.result_len()).slice().buffer;
  const result = { width: core.result_width(), height: core.result_height(), stride: core.result_width() * 4, rgba };
  if (!thumbnail) Object.assign(result, {
    x: core.result_x() >>> 0, y: core.result_y() >>> 0,
    pageWidth: core.result_page_width() >>> 0, pageHeight: core.result_page_height() >>> 0,
  });
  return result;
}

async function render(message) {
  const thumbnail = message.type === 'thumbnail';
  const parsed = thumbnail ? { page: integer(message.page, 0, core.page_count() - 1) } : options(message);
  const { page } = parsed;
  stop();
  // A completed job only helps same-page restarts. Release it before loading
  // another page: its layers/output would otherwise compete with the new input.
  if (thumbnail || page !== lastPage) releaseJob();
  trimCache(page);
  const token = begin(message.id);
  active = token;
  const timing = renderTiming = { steps: 0, maxStepMs: 0 };
  try {
    await prepare(page, thumbnail ? scopes.thumbnail : scopes.includes, token);
    if (active !== token) return;
    if (!startRender(parsed, thumbnail)) {
      active = null;
      reply(message.id, null);
      return;
    }
    // Let queued cancellation/replacement run after the first incomplete step.
    // Later turns share a short time allowance across bounded core calls.
    let deadline = 0;
    while (active === token) {
      const started = performance.now();
      const status = core.render_step(2048);
      const ended = performance.now();
      timing.maxStepMs = Math.max(timing.maxStepMs, ended - started);
      timing.steps++;
      if (status === 1) {
        if (ended >= deadline) {
          await yieldTask();
          deadline = performance.now() + 4;
        }
        continue;
      }
      check(status);
      const result = copyRaster(thumbnail);
      active = null;
      lastPage = thumbnail ? undefined : page;
      postMessage({ id: message.id, result }, [result.rgba]);
      return;
    }
  } catch (error) {
    if (active === token) {
      active = null;
      releaseJob();
      throw error;
    }
  } finally { operations.delete(token); }
}

async function metadata(message) {
  if (metadataActive) throw new Error('Busy');
  check(core.metadata_start());
  const token = begin(message.id);
  metadataActive = token;
  try {
    let deadline = 0;
    while (!token.controller.signal.aborted) {
      const status = core.metadata_step(128);
      if (status !== 1) {
        check(status);
        const bytes = new Uint8Array(core.memory.buffer, core.metadata_ptr(), core.metadata_len());
        const result = JSON.parse(new TextDecoder('utf-8', { fatal: true }).decode(bytes));
        postMessage({ id: message.id, result });
        return;
      }
      const ptr = core.metadata_range();
      check(core.last_status());
      if (ptr) {
        const view = new DataView(core.memory.buffer, ptr, 12);
        const index = view.getUint32(0, true);
        await loadBytes(index === 0xffffffff ? null : index, {
          offset: view.getUint32(4, true), length: view.getUint32(8, true),
        }, token, true);
      }
      if (performance.now() >= deadline) {
        await yieldTask();
        deadline = performance.now() + 4;
      }
    }
    throw new Error('Cancelled');
  } finally {
    operations.delete(token);
    if (metadataActive === token) {
      metadataActive = null;
      core.metadata_release();
    }
  }
}

async function openDocument(message) {
  invalidate();
  core.close();
  renderTiming = { steps: 0, maxStepMs: 0 };
  source = message.source;
  const limit = integer(message.memoryLimit, 1, 256 * 1024 * 1024);
  cacheLimit = integer(message.cacheLimit, 0, limit);
  const token = begin(message.id);
  opening = token;
  try {
    if (message.size !== undefined) {
      check(core.source_start(integer(message.size, 1, 0xffffffff), limit));
      while (true) {
        const ptr = core.source_range();
        check(core.last_status());
        if (!ptr) break;
        const view = new DataView(core.memory.buffer, ptr, 8);
        await loadBytes(null, { offset: view.getUint32(0, true), length: view.getUint32(4, true) }, token);
      }
    } else {
      if (!(message.bytes instanceof ArrayBuffer)) throw new Error('InvalidArgument');
      if (!message.bytes.byteLength || message.bytes.byteLength > 128 * 1024 * 1024) throw new Error('LimitExceeded');
      const ptr = core.input_alloc(message.bytes.byteLength, limit);
      if (!ptr) check(core.last_status());
      new Uint8Array(core.memory.buffer, ptr, message.bytes.byteLength).set(new Uint8Array(message.bytes));
      check(core.open());
    }
  } catch (error) {
    if (message.source === source) core.close();
    throw error;
  } finally {
    operations.delete(token);
    if (opening === token) opening = undefined;
  }
  const pages = Array.from({ length: core.page_count() }, (_, i) => {
    const metadata = component(core.page_component(i));
    const { id, name, title, loaded } = metadata;
    const page = { id, name, title, loaded };
    if (!loaded) return page;
    const width = core.page_width(i);
    if (core.last_status()) return { ...page, error: errors[core.last_status()] };
    return { ...page, width, height: core.page_height(i), rotation: core.page_rotation(i) };
  });
  reply(message.id, { pages, indirect: !!core.document_indirect() });
}

self.onmessage = async ({ data: message }) => {
  try {
    if (message.type === 'init') {
      if (core) throw new Error('InvalidArgument');
      core = (await WebAssembly.instantiate(message.module, {})).exports;
      reply(message.id, null);
      return;
    }
    if (!core) throw new Error('InvalidArgument');
    if (message.type === 'component-response') {
      response(message);
      return;
    }
    switch (message.type) {
      case 'open': await openDocument(message); break;
      case 'render': case 'thumbnail': await render(message); break;
      case 'geometry': {
        const parsed = options(message);
        await read(message, parsed.page, scopes.page, () => reply(message.id, geometry(parsed)));
        break;
      }
      case 'text': {
        const page = integer(message.page, 0, core.page_count() - 1);
        await read(message, page, scopes.includes, () => text(message.id, page));
        break;
      }
      case 'annotations': {
        const page = integer(message.page, 0, core.page_count() - 1);
        await read(message, page, scopes.includes, () => readJson(message.id, 'annotations', page));
        break;
      }
      case 'metadata': await metadata(message); break;
      case 'cancel-metadata': stopMetadata(); reply(message.id); break;
      case 'outline': readJson(message.id, 'outline'); break;
      case 'resolve-link': resolveLink(message); break;
      case 'cancel':
        stop();
        reply(message.id);
        break;
      case 'drop-cache':
        invalidate();
        if (opening) core.close(); else check(core.drop_components());
        reply(message.id);
        break;
      case 'diagnostics':
        reply(message.id, {
          ...renderTiming, liveBytes: core.live_bytes(), peakBytes: core.peak_bytes(),
          cacheBytes: core.cache_bytes(), linearBytes: core.memory.buffer.byteLength,
          dictionaryDecodes: core.dictionary_decodes(),
        });
        break;
      case 'close':
        invalidate();
        core.close();
        renderTiming = { steps: 0, maxStepMs: 0 };
        reply(message.id);
        break;
      default: throw new Error('InvalidArgument');
    }
  } catch (error) {
    fail(message.id, error);
  }
};
