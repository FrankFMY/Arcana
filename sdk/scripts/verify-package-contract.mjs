import assert from 'node:assert/strict';
import { spawn } from 'node:child_process';
import { createHash } from 'node:crypto';
import { copyFile, lstat, mkdtemp, readFile, realpath, rm, writeFile } from 'node:fs/promises';
import { createRequire } from 'node:module';
import { tmpdir } from 'node:os';
import { basename, dirname, join, resolve, sep } from 'node:path';
import { createInterface } from 'node:readline';
import { fileURLToPath } from 'node:url';

const sdk = resolve(dirname(fileURLToPath(import.meta.url)), '..');
const repository = dirname(sdk);
const require = createRequire(import.meta.url);
const name = '@frankfmy/arcana-sdk';
const version = '0.1.0';
const mode = process.argv[2] ?? 'runtime';
assert.ok(['runtime', 'browser'].includes(mode) && process.argv.length <= 3, 'Use no argument, or browser');
assert.ok([22, 24].includes(Number(process.versions.node.split('.')[0])), 'Qualification requires Node 22 or 24');
const owned = new Set();
const running = new Set();
let interrupted = false;
function interrupt() {
  interrupted = true;
  for (const task of running) if (!task.done) task.child.kill('SIGTERM');
}
process.once('SIGINT', interrupt);
process.once('SIGTERM', interrupt);

async function deadline(promise, milliseconds, label) {
  let timer;
  try {
    return await Promise.race([promise, new Promise((_, reject) => {
      timer = setTimeout(() => reject(new Error(`Timed out: ${label}`)), milliseconds);
    })]);
  } finally { clearTimeout(timer); }
}

function start(command, args, { cwd = repository, env = {}, capture = false } = {}) {
  assert.equal(interrupted, false, 'Qualification interrupted');
  const child = spawn(command, args, {
    cwd, env: { ...process.env, NODE_PATH: '', ...env },
    stdio: ['ignore', capture ? 'pipe' : 'inherit', capture ? 'pipe' : 'inherit'],
  });
  const task = { child, done: false, stdout: '', stderr: '', error: null };
  for (const stream of ['stdout', 'stderr']) child[stream]?.on('data', data => {
    task[stream] = (task[stream] + data.toString()).slice(-1_048_576);
  });
  task.closed = new Promise(resolve => {
    child.once('error', error => { task.error = error; });
    child.once('close', (code, signal) => { task.done = true; resolve({ code, signal }); });
  });
  running.add(task);
  return task;
}

async function stop(task) {
  if (!task.done) {
    task.child.kill('SIGTERM');
    try { await deadline(task.closed, 5_000, 'child termination'); }
    catch {
      task.child.kill('SIGKILL');
      await deadline(task.closed, 5_000, 'child reap');
    }
  }
  await task.closed;
  running.delete(task);
}

async function run(command, args, options = {}) {
  const task = start(command, args, options);
  try {
    const result = await deadline(task.closed, options.timeout ?? 120_000, `${command} ${args[0]}`);
    if (task.error) throw task.error;
    assert.equal(result.code, 0, `${command} failed (${result.signal ?? result.code})\n${task.stderr}\n${task.stdout}`);
    return task.stdout;
  } finally { await stop(task); }
}

async function scratch(label) {
  const base = await realpath(tmpdir());
  const root = await realpath(repository);
  assert.ok(base !== root && !base.startsWith(root + sep), 'Temporary apps must be outside the repository');
  const directory = await mkdtemp(join(base, `arcana-${label}-`));
  owned.add(directory);
  return directory;
}

async function removeOwned(directory) {
  assert.ok(owned.has(directory), 'Refusing to remove an unowned directory');
  await rm(directory, { recursive: true, force: true });
  owned.delete(directory);
}

function packFilename(stdout) {
  // npm versions return either an array or a name-keyed object. Lifecycle
  // output can precede the JSON; only accept a complete trailing JSON value.
  for (const offset of new Set([0, ...[...stdout.matchAll(/^[\[{]/gm)].map(match => match.index)])) {
    let value;
    try { value = JSON.parse(stdout.slice(offset)); } catch { continue; }
    const entries = Array.isArray(value) ? value : value?.filename ? [value] : Object.values(value ?? {});
    const files = entries.filter(entry => typeof entry?.filename === 'string');
    if (files.length === 1) {
      const filename = files[0].filename;
      assert.equal(filename, basename(filename));
      assert.ok(filename.endsWith('.tgz'));
      return filename;
    }
  }
  throw new Error('npm pack did not return exactly one tarball filename');
}

async function artifact(directory) {
  const override = process.env.CONSUMER_PACKAGE;
  if (override === `${name}@${version}`) return override;
  let filename;
  if (override !== undefined) {
    assert.ok(override.endsWith('.tgz') && !override.includes('://'), `CONSUMER_PACKAGE must be a .tgz path or ${name}@${version}`);
    const source = resolve(override);
    assert.ok((await lstat(source)).isFile(), 'The sealed artifact must be a regular tarball file');
    filename = join(directory, 'consumer-package.tgz');
    await writeFile(filename, await readFile(source), { mode: 0o600, flag: 'wx' });
  } else {
    const stdout = await run('npm', ['pack', '--json', '--pack-destination', directory], { cwd: sdk, capture: true });
    filename = join(directory, packFilename(stdout));
  }
  assert.ok((await lstat(filename)).isFile());
  const hash = createHash('sha256').update(await readFile(filename)).digest('hex');
  console.log(`qualification artifact ${name}@${version} sha256:${hash}`);
  return filename;
}

async function inspectPackage(app) {
  const packageRoot = join(app, 'node_modules', '@frankfmy', 'arcana-sdk');
  assert.ok((await lstat(packageRoot)).isDirectory(), 'The consumer must contain an installed package, not a source link');
  const metadata = JSON.parse(await readFile(join(packageRoot, 'package.json'), 'utf8'));
  assert.equal(metadata.name, name);
  assert.equal(metadata.version, version);
  assert.equal(metadata.type, 'module');
  assert.equal(metadata.license, 'MIT');
  assert.equal(metadata.engines.node, '>=22');
  const entries = { '.': 'index', './adapters/react': 'adapters/react', './adapters/vue': 'adapters/vue', './adapters/svelte': 'adapters/svelte' };
  assert.deepEqual(Object.keys(metadata.exports).sort(), Object.keys(entries).sort());
  const installed = await realpath(packageRoot);
  for (const [entry, stem] of Object.entries(entries)) {
    assert.deepEqual(metadata.exports[entry], { types: `./dist/${stem}.d.ts`, import: `./dist/${stem}.js` });
    for (const target of Object.values(metadata.exports[entry])) {
      const file = await realpath(join(packageRoot, target));
      assert.ok(file.startsWith(installed + sep), 'Export targets must stay inside the installed package');
      assert.ok((await lstat(file)).isFile() && (await readFile(file)).length > 0);
    }
  }
  assert.deepEqual(Object.keys(metadata.peerDependencies).sort(), ['react', 'vue']);
  for (const peer of ['react', 'vue']) assert.equal(metadata.peerDependenciesMeta[peer].optional, true);
  assert.equal(metadata.dependencies?.svelte, undefined, 'The Svelte adapter must not require a runtime peer');
  const license = await readFile(join(packageRoot, 'LICENSE'), 'utf8');
  assert.match(license, /MIT License/);
  assert.match(license, /Permission is hereby granted, free of charge/);
  console.log('installed metadata/exports/license contract: PASS');
  return packageRoot;
}

async function withServer(binary, packageRoot, body) {
  const task = start(binary, ['--package-root', packageRoot], { capture: true });
  const lines = createInterface({ input: task.child.stdout });
  try {
    const ready = new Promise(resolve => {
      lines.on('line', line => {
        try { const value = JSON.parse(line); if (typeof value.url === 'string') resolve(value.url); }
        catch { /* Engine logs are not the startup message. */ }
      });
    });
    const url = await deadline(Promise.race([ready, task.closed.then(() => {
      throw new Error(`Fixture exited before startup: ${task.error ?? task.stderr}`);
    })]), 15_000, 'Go fixture startup');
    const parsed = new URL(url);
    assert.equal(parsed.protocol, 'http:');
    assert.equal(parsed.hostname, '127.0.0.1');
    assert.ok(Number(parsed.port) > 0);
    assert.equal(parsed.pathname, '/');
    await body(url);
  } finally {
    lines.close();
    await stop(task);
    assert.equal((await task.closed).code, 0, `Go fixture did not stop cleanly: ${task.error ?? task.stderr}`);
  }
}

const peers = {
  core: [],
  react: ['react@19.2.4', 'react-dom@19.2.4', '@types/react@19.2.14', '@types/react-dom@19.2.3', 'jsdom@28.1.0'],
  vue: ['vue@3.5.42', 'jsdom@28.1.0'],
  svelte: ['svelte@5.57.2', 'jsdom@28.1.0'],
};
const peerTypes = {
  react: `
import { createElement, type ReactNode } from 'react';
import { createRoot } from 'react-dom/client';
import { ArcanaClient, type Ref, type ConnectionStatus } from '@frankfmy/arcana-sdk';
import { ArcanaProvider, useArcana, useView, useRow, useStatus, useMutation } from '@frankfmy/arcana-sdk/adapters/react';
declare const client: ArcanaClient;
const element: ReactNode = createElement(ArcanaProvider, { client, children: createElement('span') });
createRoot(document.createElement('div')).render(element);
const current: ArcanaClient = useArcana();
const view = useView('items', { scope: 'a' });
const refs: Ref[] = view.data;
const row: Record<string, unknown> | null = useRow('items', 'row');
const status: ConnectionStatus = useStatus();
const result: Promise<{ name: string } | undefined> = useMutation<{ name: string }>('rename').mutate({ name: 'After' });
// @ts-expect-error React view parameters must be a record.
useView('items', 'a');
// @ts-expect-error React row keys must be strings.
useRow('items', 123);
`,
  vue: `
import { ref, type Ref as VueRef, type DeepReadonly } from 'vue';
import { ArcanaClient, type ConnectionStatus } from '@frankfmy/arcana-sdk';
import { provideArcana, useArcana, useView, useRow, useStatus, useMutation } from '@frankfmy/arcana-sdk/adapters/vue';
declare const client: ArcanaClient;
provideArcana(client);
const current: ArcanaClient = useArcana();
const view = useView(ref('items'), ref({ scope: 'a' }));
const row: VueRef<Record<string, unknown> | null> = useRow('items', ref('row'), view.version);
const version: number = view.version.value;
const status: DeepReadonly<VueRef<ConnectionStatus>> = useStatus();
const result: Promise<{ name: string } | undefined> = useMutation<{ name: string }>('rename').mutate({ name: 'After' });
// @ts-expect-error Vue parameters are supplied through a ref or getter.
useView('items', { scope: 'a' });
// @ts-expect-error Vue row triggers contain numeric versions.
useRow('items', 'row', ref('wrong'));
`,
  svelte: `
import type { Readable } from 'svelte/store';
import type { ArcanaClient } from '@frankfmy/arcana-sdk';
import { createSubscription, type SubscriptionSnapshot } from '@frankfmy/arcana-sdk/adapters/svelte';
declare const client: ArcanaClient;
const subscription = createSubscription(client, 'items', { scope: 'a' });
const store: Readable<SubscriptionSnapshot> = subscription;
const stop: () => void = store.subscribe(snapshot => { const version: number = snapshot.version; });
subscription.destroy();
// @ts-expect-error Svelte store subscribers receive snapshots.
store.subscribe((snapshot: string) => {});
`,
};

async function types(app, kind) {
  const compiler = require.resolve('typescript/bin/tsc');
  assert.equal(require('typescript/package.json').version, '6.0.3');
  await copyFile(join(sdk, 'test/consumer-types.mts'), join(app, 'consumer-types.mts'));
  if (peerTypes[kind]) await writeFile(join(app, 'peer-types.mts'), peerTypes[kind]);
  await writeFile(join(app, 'tsconfig.json'), JSON.stringify({
    compilerOptions: {
      strict: true, noEmit: true, module: 'NodeNext', moduleResolution: 'NodeNext',
      target: 'ES2022', lib: ['ES2022', 'DOM', 'DOM.Iterable'], types: [], skipLibCheck: false,
    }, include: ['*.mts'],
  }));
  await run(process.execPath, [compiler, '--project', join(app, 'tsconfig.json')], { cwd: app, timeout: 60_000 });
  console.log(`installed positive/negative TypeScript contract: PASS (${kind}, TypeScript 6.0.3)`);
}

async function withApp(kind, packageSpec, body) {
  const app = await scratch(kind);
  try {
    await writeFile(join(app, 'package.json'), JSON.stringify({ name: `arcana-${kind}-consumer`, private: true, type: 'module' }));
    await run('npm', [
      'install', '--ignore-scripts', '--no-audit', '--no-fund', '--omit=peer',
      '--package-lock=false', '--save-exact', packageSpec, ...peers[kind],
    ], { cwd: app, timeout: 300_000 });
    const packageRoot = await inspectPackage(app);
    await copyFile(join(sdk, 'test/consumer.mjs'), join(app, 'consumer.mjs'));
    await body(app, packageRoot);
  } finally { await removeOwned(app); }
}

try {
  const artifacts = await scratch('qualification');
  const packageSpec = await artifact(artifacts);
  const binary = join(artifacts, 'fixture-server');
  await run('go', ['build', '-p=1', '-o', binary, './sdk/test/server'], {
    cwd: repository, env: { GOTOOLCHAIN: 'local' }, timeout: 180_000,
  });
  if (mode === 'runtime') {
    for (const kind of ['core', 'react', 'vue', 'svelte']) {
      await withApp(kind, packageSpec, async (app, packageRoot) => {
        await types(app, kind);
        if (kind === 'core') {
          for (const protocol of ['ws', 'http']) await withServer(binary, packageRoot, url => run(
            process.execPath, ['consumer.mjs', protocol], { cwd: app, env: { ARCANA_FIXTURE_URL: url }, timeout: 45_000 },
          ));
        } else {
          await copyFile(join(sdk, 'test/peer-consumer.mjs'), join(app, 'peer-consumer.mjs'));
          await withServer(binary, packageRoot, url => run(process.execPath,
            [...(kind === 'svelte' ? ['--conditions=browser'] : []), 'peer-consumer.mjs', kind],
            { cwd: app, env: { ARCANA_FIXTURE_URL: url }, timeout: 45_000 },
          ));
        }
      });
    }
  } else {
    assert.equal(require('playwright/package.json').version, '1.63.0');
    await withApp('core', packageSpec, async (app, packageRoot) => {
      await copyFile(join(sdk, 'test/browser-consumer.mjs'), join(app, 'browser-consumer.mjs'));
      for (const browser of ['chromium', 'webkit']) await withServer(binary, packageRoot, url => run(
        process.execPath, ['browser-consumer.mjs', browser], {
          cwd: app, env: { ARCANA_FIXTURE_URL: url, ARCANA_PLAYWRIGHT_MODULE: import.meta.resolve('playwright') }, timeout: 90_000,
        },
      ));
    });
  }
} finally {
  try { for (const task of running) await stop(task); }
  finally {
    for (const directory of [...owned]) await removeOwned(directory);
    process.removeListener('SIGINT', interrupt);
    process.removeListener('SIGTERM', interrupt);
  }
}
console.log(`installed ${mode} package contract: PASS`);
