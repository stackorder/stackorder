import { describe, expect, it } from 'vitest';

import type { GraphView } from '../api/types';
import { clone, graph, graphRun, planRun, run } from '../fixtures';
import {
  edgeClass,
  edgePath,
  groupWaves,
  hexagonPoints,
  layoutGraph,
  NODE_HEIGHT,
  nodeId,
  normalizeStackKey,
  type GraphLayout,
  type LayoutNode,
} from './layout';

function node(layout: GraphLayout, id: string): LayoutNode {
  const found = layout.nodes.find((n) => n.id === id);
  if (!found) throw new Error(`no node ${id}`);
  return found;
}

describe('edgeClass', () => {
  it.each([
    [{ type: 'depends_on' as const }, {}, 'edge edge--depends_on'],
    [{ type: 'reads_state' as const, inferred: true }, {}, 'edge edge--reads_state edge--inferred'],
    [{ type: 'reads_state' as const, inferred: false }, {}, 'edge edge--reads_state'],
    [{ type: 'uses_module' as const }, {}, 'edge edge--uses_module'],
    [{ type: 'depends_on' as const, inferred: true }, {}, 'edge edge--depends_on edge--inferred'],
    [{ type: 'depends_on' as const }, { affected: true }, 'edge edge--depends_on edge--affected'],
    [{ type: 'uses_module' as const }, { dimmed: true }, 'edge edge--uses_module edge--dimmed'],
  ])('%j with %j is "%s"', (edge, state, want) => {
    expect(edgeClass(edge, state)).toBe(want);
  });
});

describe('groupWaves', () => {
  it('uses the explicit waves, sorted, with statuses and ids', () => {
    const statuses = new Map((run.stacks ?? []).map((s) => [s.key, s.status]));
    expect(groupWaves(graphRun, statuses)).toEqual([
      {
        wave: 0,
        stacks: [
          { key: 'stacks/prod/vpc', status: 'applied', stackId: '8a2d4f60-1c3b-4e5a-b7d9-0e1f2a3b4c22' },
          { key: 'stacks/staging/vpc', status: 'applied', stackId: '3f6c1a2e-7b1d-4c3e-9a51-2b8d4e6f0a11' },
        ],
      },
      {
        wave: 1,
        stacks: [
          { key: 'stacks/prod/eks', status: 'failed', stackId: 'c4d6e8f0-3a5b-4c7d-9e1f-2a4b6c8d0e44' },
          { key: 'stacks/staging/eks', status: 'applied', stackId: '5b7e9c01-2d4f-4a6b-8c0e-1f3a5b7d9e33' },
        ],
      },
      { wave: 2, stacks: [{ key: 'stacks/prod/apps', status: 'blocked', stackId: 'e1f3a5b7-4c6d-4e8f-a0b2-3c5d7e9f1a55' }] },
    ]);
  });

  it.each<[string, Pick<GraphView, 'waves' | 'affected' | 'stack_ids'>, { wave: number; keys: string[] }[]]>([
    ['nothing to replay', {}, []],
    ['empty waves and no affected', { waves: [], affected: [] }, []],
    [
      'affected only, with a gap',
      {
        affected: [
          { key: 'c', path: 'c', wave: 2, reasons: ['dependent'] },
          { key: 'a', path: 'a', wave: 0, reasons: ['changed'] },
          { key: 'b', path: 'b', wave: 0, reasons: null },
        ],
      },
      [
        { wave: 0, keys: ['a', 'b'] },
        { wave: 1, keys: [] },
        { wave: 2, keys: ['c'] },
      ],
    ],
    [
      'explicit waves win over affected',
      { waves: [['x', 'x']], affected: [{ key: 'y', path: 'y', wave: 3, reasons: [] }] },
      [{ wave: 0, keys: ['x'] }],
    ],
  ])('%s', (_name, view, want) => {
    expect(groupWaves(view).map((w) => ({ wave: w.wave, keys: w.stacks.map((s) => s.key) }))).toEqual(want);
  });
});

describe('normalizeStackKey and nodeId', () => {
  it.each([
    ['stacks/prod/vpc', 'acme/infra', 'stacks/prod/vpc'],
    ['acme/infra//stacks/prod/vpc', 'acme/infra', 'stacks/prod/vpc'],
    ['acme/network-infra//stacks/prod/tgw', 'acme/infra', 'acme/network-infra//stacks/prod/tgw'],
    ['stacks/prod/vpc:blue', 'acme/infra', 'stacks/prod/vpc:blue'],
  ])('%s in %s is %s', (key, repo, want) => {
    expect(normalizeStackKey(key, repo)).toBe(want);
  });

  it('prefixes ids with the node kind', () => {
    expect(nodeId({ kind: 'stack', key: 'modules/vpc' })).toBe('stack:modules/vpc');
    expect(nodeId({ kind: 'module', key: 'modules/vpc' })).toBe('module:modules/vpc');
  });
});

describe('layoutGraph', () => {
  it('lays out the example graph with stacks, modules and every edge kind', () => {
    const layout = layoutGraph(graph);
    expect(layout.replay).toBe(false);
    expect(layout.waves).toEqual([]);
    expect(layout.nodes.filter((n) => n.kind === 'stack').map((n) => n.key)).toEqual([
      'stacks/staging/vpc',
      'stacks/prod/vpc',
      'stacks/staging/eks',
      'stacks/prod/eks',
      'stacks/prod/apps',
    ]);
    expect(layout.nodes.filter((n) => n.kind === 'module').map((n) => n.label)).toEqual([
      'modules/vpc',
      'modules/eks',
      'acme/terraform-modules//eks-addons@v0.8.0',
    ]);
    expect(layout.edges.map((e) => e.className)).toEqual([
      'edge edge--uses_module',
      'edge edge--uses_module',
      'edge edge--uses_module',
      'edge edge--uses_module',
      'edge edge--uses_module',
      'edge edge--depends_on',
      'edge edge--depends_on',
      'edge edge--reads_state edge--inferred',
    ]);
    expect(layout.width).toBeGreaterThan(0);
    expect(layout.height).toBeGreaterThan(0);
    for (const n of layout.nodes) {
      expect(n.height).toBe(NODE_HEIGHT);
      expect(n.x - n.width / 2).toBeGreaterThanOrEqual(0);
      expect(n.x + n.width / 2).toBeLessThanOrEqual(layout.width);
      expect(n.affected).toBe(false);
    }
    for (const e of layout.edges) expect(e.points.length).toBeGreaterThanOrEqual(2);
  });

  it('draws edges from the dependency to the dependent, left to right', () => {
    const layout = layoutGraph(graph);
    const dependsOn = layout.edges.find((e) => e.target === 'stack:stacks/prod/eks' && e.type === 'depends_on');
    expect(dependsOn?.source).toBe('stack:stacks/prod/vpc');
    const readsState = layout.edges.find((e) => e.type === 'reads_state');
    expect(readsState).toMatchObject({ source: 'stack:stacks/prod/eks', target: 'stack:stacks/prod/apps', inferred: true });
    const nested = layout.edges.find((e) => e.ref === 'v0.8.0');
    expect(nested).toMatchObject({
      source: 'module:acme/terraform-modules//eks-addons@v0.8.0',
      target: 'module:acme/infra//modules/eks',
    });
    const x = (id: string) => node(layout, id).x;
    expect(x('module:acme/infra//modules/vpc')).toBeLessThan(x('stack:stacks/prod/vpc'));
    expect(x('stack:stacks/prod/vpc')).toBeLessThan(x('stack:stacks/prod/eks'));
    expect(x('stack:stacks/prod/eks')).toBeLessThan(x('stack:stacks/prod/apps'));
  });

  it('carries stack ids and details for navigation', () => {
    const layout = layoutGraph(graph);
    expect(node(layout, 'stack:stacks/prod/eks')).toMatchObject({
      stackId: 'c4d6e8f0-3a5b-4c7d-9e1f-2a4b6c8d0e44',
      detail: 'production · tofu',
      external: false,
    });
    expect(node(layout, 'module:acme/infra//modules/vpc')).toMatchObject({ moduleKind: 'local', detail: 'local module' });
  });

  it('replays a run: affected set, waves, statuses and dimmed edges', () => {
    const layout = layoutGraph(graphRun, run);
    expect(layout.replay).toBe(true);
    expect(layout.waves.map((w) => w.stacks.map((s) => `${s.key}=${s.status ?? ''}`))).toEqual([
      ['stacks/prod/vpc=applied', 'stacks/staging/vpc=applied'],
      ['stacks/prod/eks=failed', 'stacks/staging/eks=applied'],
      ['stacks/prod/apps=blocked'],
    ]);
    expect(node(layout, 'stack:stacks/prod/eks')).toMatchObject({
      affected: true,
      wave: 1,
      status: 'failed',
      reasons: ['dependent'],
      detail: 'failed · wave 1',
    });
    expect(node(layout, 'stack:stacks/prod/apps')).toMatchObject({ wave: 2, status: 'blocked' });
    expect(node(layout, 'module:acme/infra//modules/vpc').affected).toBe(true);
    expect(node(layout, 'module:acme/infra//modules/eks').affected).toBe(false);
    expect(node(layout, 'module:acme/terraform-modules//eks-addons@v0.8.0').affected).toBe(false);

    const byTarget = (source: string, target: string) => layout.edges.find((e) => e.source === source && e.target === target);
    expect(byTarget('module:acme/infra//modules/vpc', 'stack:stacks/prod/vpc')?.className).toBe(
      'edge edge--uses_module edge--affected',
    );
    expect(byTarget('stack:stacks/prod/eks', 'stack:stacks/prod/apps')?.className).toBe(
      'edge edge--reads_state edge--inferred edge--affected',
    );
    expect(byTarget('module:acme/infra//modules/eks', 'stack:stacks/prod/eks')?.className).toBe(
      'edge edge--uses_module edge--dimmed',
    );
  });

  it('replays without a run detail using the affected set alone', () => {
    const layout = layoutGraph(graphRun);
    expect(layout.replay).toBe(true);
    expect(node(layout, 'stack:stacks/prod/eks')).toMatchObject({ affected: true, detail: 'affected · wave 1' });
    expect(node(layout, 'stack:stacks/prod/eks').status).toBeUndefined();
  });

  it('colours stacks from a run even when the graph has no affected set', () => {
    const layout = layoutGraph(graph, planRun);
    expect(layout.replay).toBe(true);
    expect(node(layout, 'stack:stacks/prod/apps')).toMatchObject({ affected: true, status: 'planned' });
  });

  it('adds placeholder nodes for cross-repo edges and normalises own-repo qualified keys', () => {
    const view = clone(graph);
    view.graph.edges = [
      ...(view.graph.edges ?? []),
      {
        from: { kind: 'stack', key: 'acme/infra//stacks/prod/vpc' },
        to: { kind: 'stack', key: 'acme/network-infra//stacks/prod/tgw' },
        type: 'depends_on',
      },
      { from: { kind: 'stack', key: 'stacks/prod/apps' }, to: { kind: 'module', key: 'registry:hashicorp/consul/aws@0.12' }, type: 'uses_module' },
      { from: { kind: 'stack', key: 'stacks/prod/apps' }, to: { kind: 'stack', key: 'stacks/prod/apps' }, type: 'depends_on' },
    ];
    const layout = layoutGraph(view);
    const tgw = node(layout, 'stack:acme/network-infra//stacks/prod/tgw');
    expect(tgw).toMatchObject({ external: true, repo: 'acme/network-infra', detail: 'external stack' });
    expect(tgw.stackId).toBeUndefined();
    expect(node(layout, 'module:registry:hashicorp/consul/aws@0.12').label).toBe('hashicorp/consul/aws@0.12');
    expect(layout.edges.find((e) => e.source === tgw.id)?.target).toBe('stack:stacks/prod/vpc');
    expect(layout.nodes.filter((n) => n.key === 'stacks/prod/vpc')).toHaveLength(1);
    expect(layout.edges.some((e) => e.source === e.target)).toBe(false);
  });

  it('keeps external stacks of the graph qualified by their repository', () => {
    const view = clone(graph);
    view.graph.stacks = [...(view.graph.stacks ?? []), { key: 'stacks/shared/dns', path: 'stacks/shared/dns', repo: 'acme/dns', external: true }];
    const layout = layoutGraph(view);
    expect(node(layout, 'stack:acme/dns//stacks/shared/dns')).toMatchObject({ external: true, repo: 'acme/dns' });
  });

  it('tolerates a graph with null lists', () => {
    const layout = layoutGraph({ repo: 'acme/empty', sha: 'abc', graph: { repo: 'acme/empty', sha: 'abc', stacks: null, modules: null, edges: null } });
    expect(layout.nodes).toEqual([]);
    expect(layout.edges).toEqual([]);
    expect(layout.width).toBeGreaterThan(0);
  });
});

describe('edgePath', () => {
  it.each([
    [[], ''],
    [[{ x: 0, y: 0 }], 'M0,0'],
    [[{ x: 0, y: 0 }, { x: 10, y: 5 }], 'M0,0L10,5'],
    [
      [
        { x: 0, y: 0 },
        { x: 60, y: 0 },
        { x: 120, y: 60 },
      ],
      'M0,0L10,0C20,0 40,0 60,10C80,20 100,40 110,50L120,60',
    ],
  ])('%j', (points, want) => {
    expect(edgePath(points)).toBe(want);
  });
});

describe('hexagonPoints', () => {
  it('insets the side vertices by half the height', () => {
    expect(hexagonPoints(200, 52)).toBe('26,0 174,0 200,26 174,52 26,52 0,26');
  });
});
