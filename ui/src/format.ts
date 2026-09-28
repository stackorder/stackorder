import { splitQualifiedStackKey, type Backend, type Module, type PlanSummary } from './api/types';

/** Default GitHub web origin, used when the API gives no link to derive one from. */
export const GITHUB_ORIGIN = 'https://github.com';

/** Shortens a commit SHA for display. */
export function shortSha(sha: string | undefined): string {
  return sha ? sha.slice(0, 7) : '';
}

/** Shortens a UUID to its first block for display. */
export function shortId(id: string): string {
  return id.split('-')[0] ?? id;
}

/** Returns the web origin of a GitHub link, so GitHub Enterprise links stay on their host. */
export function githubOrigin(link: string | undefined): string {
  if (!link) return GITHUB_ORIGIN;
  try {
    return new URL(link).origin;
  } catch {
    return GITHUB_ORIGIN;
  }
}

/** Link to a pull request. */
export function pullUrl(repo: string, number: number, origin = GITHUB_ORIGIN): string {
  return `${origin}/${repo}/pull/${number}`;
}

/** Link to a commit. */
export function commitUrl(repo: string, sha: string, origin = GITHUB_ORIGIN): string {
  return `${origin}/${repo}/commit/${sha}`;
}

/** Link to an issue. */
export function issueUrl(repo: string, number: number, origin = GITHUB_ORIGIN): string {
  return `${origin}/${repo}/issues/${number}`;
}

/** The S3 object key of a stack's state, including the workspace prefix. */
export function stateObjectKey(backend: Backend, workspace?: string): string | undefined {
  if (!backend.key) return undefined;
  if (!workspace || workspace === 'default') return backend.key;
  const prefix = backend.workspace_key_prefix ?? 'env:';
  return `${prefix}/${workspace}/${backend.key}`;
}

/** Link to a stack's state object in the S3 console, or undefined for non-S3 backends. */
export function s3ConsoleUrl(backend: Backend | undefined, workspace?: string): string | undefined {
  if (backend?.type !== 's3' || !backend.bucket) return undefined;
  const bucket = encodeURIComponent(backend.bucket);
  const key = stateObjectKey(backend, workspace);
  const params = new URLSearchParams();
  if (backend.region) params.set('region', backend.region);
  if (!key) {
    const qs = params.toString();
    return `https://s3.console.aws.amazon.com/s3/buckets/${bucket}${qs ? `?${qs}` : ''}`;
  }
  params.set('bucketType', 'general');
  params.set('prefix', key);
  return `https://s3.console.aws.amazon.com/s3/object/${bucket}?${params.toString()}`;
}

/** The s3:// URI of a stack's state, for display. */
export function stateUri(backend: Backend, workspace?: string): string {
  const key = stateObjectKey(backend, workspace);
  return `s3://${backend.bucket ?? '?'}${key ? `/${key}` : ''}`;
}

/** Total resource actions of a plan summary. */
export function summaryTotal(s: PlanSummary): number {
  return s.adds + s.changes + s.destroys + s.replaces + (s.imports ?? 0) + (s.moves ?? 0);
}

/** A module key without its @ref suffix, which is how the modules list is searched. */
export function moduleBaseKey(key: string): string {
  const at = key.lastIndexOf('@');
  return at > key.lastIndexOf('/') ? key.slice(0, at) : key;
}

/** A short display label for a module node. */
export function moduleLabel(m: Pick<Module, 'key' | 'kind' | 'path'>): string {
  if (m.kind === 'registry') return m.key.replace(/^registry:/, '');
  if (m.kind === 'local') return m.path ?? (splitQualifiedStackKey(m.key).key || m.key);
  return m.key;
}

/** Splits "owner/repo" into its parts. */
export function splitRepo(fullName: string): { owner: string; name: string } {
  const [owner = '', name = ''] = fullName.split('/');
  return { owner, name };
}

/** Path of a repository's graph page. */
export function repoPath(fullName: string, query?: Record<string, string | undefined>): string {
  const { owner, name } = splitRepo(fullName);
  const params = new URLSearchParams();
  for (const [k, v] of Object.entries(query ?? {})) if (v) params.set(k, v);
  const qs = params.toString();
  return `/repos/${encodeURIComponent(owner)}/${encodeURIComponent(name)}${qs ? `?${qs}` : ''}`;
}

/** Human label for a status-like identifier. */
export function humanize(value: string): string {
  return value.replace(/_/g, ' ');
}

/** English plural helper. */
export function plural(n: number, one: string, many = `${one}s`): string {
  return `${n} ${n === 1 ? one : many}`;
}
