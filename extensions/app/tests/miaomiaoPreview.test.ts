import { describe, expect, test } from 'bun:test';
import { readFileSync } from 'node:fs';
const workflow = Bun.YAML.parse(readFileSync('.github/workflows/miaomiao-preview.yml', 'utf8')) as any;
const release = Bun.YAML.parse(readFileSync('.github/workflows/release.yml', 'utf8')) as any;
const execute = new (Object.getPrototypeOf(async function () {}).constructor)('github', 'context', 'core', workflow.jobs.source.steps[0].with.script);
async function run(options: any = {}) {
  const repository = { full_name: 'router-for-me/EasyCLIProxyAPI' };
  const headRepo = { full_name: 'Roomen/EasyCLIProxyAPI' };
  const pr = { number: 42, user: { login: 'Roomen' }, base: { ref: 'miaomiao', repo: repository }, head: { sha: 'tested', repo: headRepo }, state: 'open', draft: false, ...options.pr };
  const build = { id: 123, event: 'pull_request', status: 'completed', conclusion: 'success', path: '.github/workflows/release.yml', repository, head_sha: 'tested', head_repository: headRepo, ...options.build };
  const jobs = ['Resolve app version', ...['linux', 'darwin', 'windows'].flatMap(os => ['amd64', 'aarch64'].map(arch => `Build ${os}-${arch}`))].map(name => ({ name, conclusion: 'success' }));
  if (options.failedJob) jobs[1].conclusion = options.failedJob;
  if (options.missingJob) jobs.pop();
  const merges: any[] = [], outputs: any = {};
  const listPR = () => {}, listJobs = () => {};
  const github = { rest: { actions: { getWorkflowRun: async () => ({ data: build }), listJobsForWorkflowRun: listJobs }, pulls: { list: listPR, get: async () => ({ data: pr }), merge: async (args: any) => { merges.push(args); if (options.error) throw new Error('409'); return { data: { merged: !options.rejected, sha: 'merged' } }; } } }, paginate: async (fn: any) => fn === listPR ? [pr] : jobs };
  const context = { repo: { owner: 'router-for-me', repo: 'EasyCLIProxyAPI' }, eventName: 'workflow_run', ref: 'refs/heads/main', sha: 'push-sha', payload: { workflow_run: { id: 123 } }, ...options.context };
  let error;
  try { await execute(github, context, { info() {}, setOutput: (key: string, value: string) => outputs[key] = value }); } catch (e) { error = e; }
  return { merges, outputs, error };
}
describe('Roomen-only miaomiao merge', () => {
  test('merges only the tested head and builds resulting commit', async () => {
    const result = await run();
    expect(result.error).toBeUndefined();
    expect(result.merges).toHaveLength(1);
    expect(result.merges[0].sha).toBe('tested');
    expect(result.outputs).toEqual({ sha: 'merged', pr: '42' });
  });
  for (const [name, options] of Object.entries({
    author: { pr: { user: { login: 'another-user' } } },
    draft: { pr: { draft: true } },
    closed: { pr: { state: 'closed' } },
    target: { pr: { base: { ref: 'main' } } },
    repository: { pr: { base: { ref: 'miaomiao', repo: { full_name: 'other/repo' } } } },
    stale: { pr: { head: { sha: 'new' } } },
    fork: { build: { head_repository: { full_name: 'other/repo' } } },
    failed: { build: { conclusion: 'failure' } },
    pending: { build: { status: 'in_progress' } },
    workflow: { build: { path: '.github/workflows/untrusted.yml' } },
    event: { build: { event: 'push' } },
    skipped: { failedJob: 'skipped' },
    failedPlatform: { failedJob: 'failure' },
    missingPlatform: { missingJob: true },
  })) test(`rejects ${name}`, async () => {
    const result = await run(options);
    expect(result.merges).toHaveLength(0);
    expect(result.outputs).toEqual({});
  });
  for (const option of [{ rejected: true }, { error: true }]) test('no preview on merge failure', async () => {
    const result = await run(option);
    expect(result.error).toBeInstanceOf(Error);
    expect(result.outputs).toEqual({});
  });
  for (const eventName of ['push', 'workflow_dispatch']) test(`${eventName} builds miaomiao only`, async () => {
    expect((await run({ context: { eventName, ref: 'refs/heads/miaomiao' } })).outputs.sha).toBe('push-sha');
    expect((await run({ context: { eventName, ref: 'refs/heads/main' } })).outputs).toEqual({});
  });
  test('deletion does not build', async () => {
    expect((await run({ context: { eventName: 'push', ref: 'refs/heads/miaomiao', payload: { deleted: true } } })).outputs).toEqual({});
  });
  test('preview publishes only after all builds, with read-only build jobs', () => {
    expect(release.jobs['publish-preview'].needs).toEqual(['version', 'build']);
    expect(release.jobs.build.permissions.contents).toBe('read');
    expect(release.jobs.version.permissions.contents).toBe('read');
    expect(release.on.workflow_call.inputs.source_sha.required).toBe(true);
  });
});
