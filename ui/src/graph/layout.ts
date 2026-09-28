import dagre, { type EdgeLabel, type GraphLabel, type NodeLabel } from '@dagrejs/dagre';

import {
  qualifiedStackKey,
  splitQualifiedStackKey,
  type Edge,
  type EdgeType,
  type GraphView,
  type ModuleKind,
  type NodeKind,
  type NodeRef,
  type Reason,
  type Run,
  type StackStatus,
} from '../api/types';
import { moduleLabel } from '../format';

/** A coordinate in graph space. */
export interface Point {
  x: number;
  y: number;
}

/** A positioned node of the laid out graph; x and y are its centre. */
export interface LayoutNode {
  id: string;
  kind: NodeKind;
  key: string;
  label: string;
  detail: string;
  x: number;
  y: number;
  width: number;
  height: number;
  stackId?: string;
  repo?: string;
  moduleKind?: ModuleKind;
  external: boolean;
  affected: boolean;
  wave?: number;
  status?: StackStatus;
  reasons: Reason[];
}

/** A routed edge, drawn from the dependency to its dependent. */
export interface LayoutEdge {
  id: string;
  source: string;
  target: string;
  type: EdgeType;
  inferred: boolean;
  className: string;
  points: Point[];
  affected: boolean;
  ref?: string;
}

/** The stacks of one wave of a replayed run. */
export interface WaveGroup {
  wave: number;
  stacks: { key: string; status?: StackStatus; stackId?: string }[];
}

/** The graph with positions, ready to render. */
export interface GraphLayout {
  nodes: LayoutNode[];
  edges: LayoutEdge[];
  width: number;
  height: number;
  waves: WaveGroup[];
  replay: boolean;
}

/** Height of every node box. */
export const NODE_HEIGHT = 52;

const CHAR_WIDTH = 7.2;
const MIN_WIDTH = 150;
const PADDING = 36;

/** The layout id of a node reference. */
export function nodeId(ref: NodeRef): string {
  return `${ref.kind}:${ref.key}`;
}

/** Drops the "owner/repo//" prefix from a stack key that belongs to repo. */
export function normalizeStackKey(key: string, repo: string): string {
  const q = splitQualifiedStackKey(key);
  return q.repo && q.repo === repo ? q.key : key;
}

/** CSS classes of an edge: its type, whether it was inferred, and its replay state. */
export function edgeClass(
  edge: Pick<Edge, 'type' | 'inferred'>,
  state: { affected?: boolean; dimmed?: boolean } = {},
): string {
  const classes = ['edge', `edge--${edge.type}`];
  if (edge.inferred) classes.push('edge--inferred');
  if (state.affected) classes.push('edge--affected');
  if (state.dimmed) classes.push('edge--dimmed');
  return classes.join(' ');
}

/** Groups a replayed run's stacks by wave, from the explicit waves or from each stack's wave. */
export function groupWaves(
  view: Pick<GraphView, 'waves' | 'affected' | 'stack_ids'>,
  statuses: ReadonlyMap<string, StackStatus> = new Map(),
): WaveGroup[] {
  let waves: string[][] = view.waves?.map((w) => [...w]) ?? [];
  if (waves.length === 0 && view.affected?.length) {
    for (const a of view.affected) {
      while (waves.length <= a.wave) waves.push([]);
      waves[a.wave]?.push(a.key);
    }
  }
  waves = waves.map((keys) => [...new Set(keys)].sort());
  return waves.map((keys, wave) => ({
    wave,
    stacks: keys.map((key) => {
      const entry: WaveGroup['stacks'][number] = { key };
      const status = statuses.get(key);
      const stackId = view.stack_ids?.[key];
      if (status) entry.status = status;
      if (stackId) entry.stackId = stackId;
      return entry;
    }),
  }));
}

/** Vertices of the elongated hexagon that marks a module node. */
export function hexagonPoints(width: number, height: number): string {
  const inset = height / 2;
  return [
    [inset, 0],
    [width - inset, 0],
    [width, height / 2],
    [width - inset, height],
    [inset, height],
    [0, height / 2],
  ]
    .map(([x, y]) => `${String(x)},${String(y)}`)
    .join(' ');
}

function round(n: number): string {
  return String(Math.round(n * 10) / 10);
}

/** An SVG path through the routed points, smoothed as a uniform B-spline. */
export function edgePath(points: readonly Point[]): string {
  if (points.length === 0) return '';
  if (points.length < 3) return `M${points.map((p) => `${round(p.x)},${round(p.y)}`).join('L')}`;
  let d = '';
  let x0 = 0;
  let y0 = 0;
  let x1 = 0;
  let y1 = 0;
  let state = 0;
  const bezier = (x: number, y: number) => {
    d += `C${round((2 * x0 + x1) / 3)},${round((2 * y0 + y1) / 3)} ${round((x0 + 2 * x1) / 3)},${round((y0 + 2 * y1) / 3)} ${round((x0 + 4 * x1 + x) / 6)},${round((y0 + 4 * y1 + y) / 6)}`;
  };
  for (const { x, y } of points) {
    if (state === 0) {
      state = 1;
      d += `M${round(x)},${round(y)}`;
    } else if (state === 1) {
      state = 2;
    } else {
      if (state === 2) {
        state = 3;
        d += `L${round((5 * x0 + x1) / 6)},${round((5 * y0 + y1) / 6)}`;
      }
      bezier(x, y);
    }
    x0 = x1;
    x1 = x;
    y0 = y1;
    y1 = y;
  }
  bezier(x1, y1);
  d += `L${round(x1)},${round(y1)}`;
  return d;
}

function textWidth(...lines: string[]): number {
  return Math.max(MIN_WIDTH, Math.ceil(Math.max(...lines.map((l) => l.length)) * CHAR_WIDTH + PADDING));
}

function stackDetail(environment: string | undefined, tool: string | undefined, external: boolean): string {
  if (external) return 'external stack';
  return [environment, tool].filter(Boolean).join(' · ') || 'stack';
}

/** Lays out a repository graph left to right, dependencies before dependents, marking a replayed run. */
export function layoutGraph(view: GraphView, run?: Run): GraphLayout {
  const repo = view.graph.repo || view.repo;
  const norm = (ref: NodeRef): NodeRef =>
    ref.kind === 'stack' ? { kind: 'stack', key: normalizeStackKey(ref.key, repo) } : ref;

  const statuses = new Map<string, StackStatus>();
  for (const s of run?.stacks ?? []) statuses.set(normalizeStackKey(s.key, repo), s.status);

  const waveOf = new Map<string, number>();
  const reasonsOf = new Map<string, Reason[]>();
  const onPath = new Set<string>();
  for (const a of view.affected ?? []) {
    const key = normalizeStackKey(a.key, repo);
    waveOf.set(key, a.wave);
    reasonsOf.set(key, a.reasons ?? []);
    for (const via of a.via ?? []) onPath.add(via);
  }
  (view.waves ?? []).forEach((keys, wave) => {
    for (const key of keys) waveOf.set(normalizeStackKey(key, repo), wave);
  });
  const replay = waveOf.size > 0 || statuses.size > 0;

  const nodes = new Map<string, LayoutNode>();
  const addNode = (node: Omit<LayoutNode, 'x' | 'y' | 'width' | 'height'>) => {
    if (nodes.has(node.id)) return;
    const width = textWidth(node.label, node.detail) + (node.kind === 'module' ? NODE_HEIGHT / 2 : 0);
    nodes.set(node.id, { ...node, x: 0, y: 0, width, height: NODE_HEIGHT });
  };
  const addStack = (key: string, extra: { environment?: string; tool?: string; external?: boolean; repo?: string }) => {
    const external = Boolean(extra.external) || splitQualifiedStackKey(key).repo !== '';
    const affected = waveOf.has(key) || statuses.has(key);
    const status = statuses.get(key);
    const wave = waveOf.get(key);
    const detail =
      replay && affected
        ? [status ?? 'affected', wave === undefined ? '' : `wave ${String(wave)}`].filter(Boolean).join(' · ')
        : stackDetail(extra.environment, extra.tool, external);
    const node: Omit<LayoutNode, 'x' | 'y' | 'width' | 'height'> = {
      id: nodeId({ kind: 'stack', key }),
      kind: 'stack',
      key,
      label: key,
      detail,
      external,
      affected,
      reasons: reasonsOf.get(key) ?? [],
    };
    const stackId = view.stack_ids?.[key];
    if (stackId) node.stackId = stackId;
    const owner = extra.repo ?? splitQualifiedStackKey(key).repo;
    if (external && owner) node.repo = owner;
    if (wave !== undefined) node.wave = wave;
    if (status) node.status = status;
    addNode(node);
  };

  for (const s of view.graph.stacks ?? []) {
    const external = Boolean(s.external) || Boolean(s.repo && s.repo !== repo);
    const key = external && s.repo && !s.key.includes('//') ? qualifiedStackKey(s.repo, s.key) : normalizeStackKey(s.key, repo);
    const extra: { environment?: string; tool?: string; external?: boolean; repo?: string } = { external };
    if (s.environment) extra.environment = s.environment;
    if (s.tool) extra.tool = s.tool;
    if (s.repo) extra.repo = s.repo;
    addStack(key, extra);
  }
  for (const m of view.graph.modules ?? []) {
    addNode({
      id: nodeId({ kind: 'module', key: m.key }),
      kind: 'module',
      key: m.key,
      label: moduleLabel(m),
      detail: `${m.kind} module`,
      moduleKind: m.kind,
      external: false,
      affected: onPath.has(m.key),
      reasons: [],
    });
  }

  const edges: LayoutEdge[] = [];
  (view.graph.edges ?? []).forEach((e, i) => {
    const from = norm(e.from);
    const to = norm(e.to);
    for (const ref of [from, to]) {
      if (nodes.has(nodeId(ref))) continue;
      if (ref.kind === 'stack') addStack(ref.key, { external: true });
      else
        addNode({
          id: nodeId(ref),
          kind: 'module',
          key: ref.key,
          label: moduleLabel({ key: ref.key, kind: ref.key.startsWith('registry:') ? 'registry' : 'git' }),
          detail: 'module',
          external: false,
          affected: onPath.has(ref.key),
          reasons: [],
        });
    }
    const source = nodeId(to);
    const target = nodeId(from);
    if (source === target) return;
    const affected = Boolean(nodes.get(source)?.affected && nodes.get(target)?.affected);
    const edge: LayoutEdge = {
      id: `e${String(i)}`,
      source,
      target,
      type: e.type,
      inferred: Boolean(e.inferred),
      className: edgeClass(e, { affected, dimmed: replay && !affected }),
      points: [],
      affected,
    };
    const ref = e.meta?.ref;
    if (ref) edge.ref = ref;
    edges.push(edge);
  });

  const g = new dagre.graphlib.Graph<GraphLabel, NodeLabel, EdgeLabel>({ multigraph: true });
  g.setGraph({ rankdir: 'LR', nodesep: 22, ranksep: 72, edgesep: 14, marginx: 24, marginy: 24 });
  g.setDefaultEdgeLabel(() => ({}));
  for (const n of nodes.values()) g.setNode(n.id, { width: n.width, height: n.height });
  for (const e of edges) g.setEdge(e.source, e.target, { minlen: 1, weight: e.type === 'uses_module' ? 1 : 2 }, e.id);
  dagre.layout(g);

  for (const n of nodes.values()) {
    const placed = g.node(n.id);
    n.x = placed.x ?? 0;
    n.y = placed.y ?? 0;
  }
  for (const e of edges) {
    e.points = (g.edge({ v: e.source, w: e.target, name: e.id }).points ?? []).map((p) => ({ x: p.x, y: p.y }));
  }

  const label = g.graph();
  return {
    nodes: [...nodes.values()],
    edges,
    width: Math.max(label.width ?? 0, 1),
    height: Math.max(label.height ?? 0, 1),
    waves: groupWaves(view, statuses),
    replay,
  };
}
