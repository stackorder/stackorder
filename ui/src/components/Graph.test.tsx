import { fireEvent, render, screen } from '@testing-library/preact';
import { describe, expect, it, vi } from 'vitest';

import { graph, graphRun, run } from '../fixtures';
import { layoutGraph } from '../graph/layout';
import { Graph, GraphLegend, nodeClass, nodeLabel, zoomAround } from './Graph';

describe('zoomAround', () => {
  it.each([
    [{ x: 0, y: 0, k: 1 }, 2, 100, 50, { x: -100, y: -50, k: 2 }],
    [{ x: -100, y: -50, k: 2 }, 0.5, 100, 50, { x: 0, y: 0, k: 1 }],
    [{ x: 0, y: 0, k: 3 }, 10, 0, 0, { x: 0, y: 0, k: 4 }],
    [{ x: 0, y: 0, k: 0.3 }, 0.1, 0, 0, { x: 0, y: 0, k: 0.25 }],
  ])('%j by %d around (%d, %d)', (view, factor, px, py, want) => {
    const got = zoomAround(view, factor, px, py);
    expect(got.k).toBeCloseTo(want.k);
    expect(got.x).toBeCloseTo(want.x);
    expect(got.y).toBeCloseTo(want.y);
  });
});

describe('nodeClass and nodeLabel', () => {
  const replayed = layoutGraph(graphRun, run);
  const find = (id: string) => {
    const n = replayed.nodes.find((x) => x.id === id);
    if (!n) throw new Error(id);
    return n;
  };

  it.each([
    ['stack:stacks/prod/eks', 'node node--stack node--affected node--tone-danger'],
    ['stack:stacks/prod/apps', 'node node--stack node--affected node--tone-warning'],
    ['module:acme/infra//modules/eks', 'node node--module node--local node--dimmed'],
    ['module:acme/infra//modules/vpc', 'node node--module node--local node--affected'],
  ])('%s is "%s"', (id, want) => {
    expect(nodeClass(find(id), true)).toBe(want);
  });

  it('spells out status, wave and reasons', () => {
    expect(nodeLabel(find('stack:stacks/prod/apps'))).toBe(
      'Stack stacks/prod/apps, status blocked, wave 2, affected because reads state',
    );
    expect(nodeLabel(find('module:acme/infra//modules/vpc'))).toBe('Module acme/infra//modules/vpc, on the change path');
  });
});

describe('Graph', () => {
  it('renders every node as a keyboard-reachable link and every edge', () => {
    const layout = layoutGraph(graph);
    const { container } = render(<Graph layout={layout} label="Dependency graph of acme/infra" onActivate={vi.fn()} />);
    expect(screen.getByRole('group', { name: 'Dependency graph of acme/infra' })).toBeInTheDocument();
    const links = screen.getAllByRole('link');
    expect(links).toHaveLength(8);
    for (const l of links) expect(l).toHaveAttribute('tabindex', '0');
    expect(container.querySelectorAll('path.edge')).toHaveLength(8);
    expect(container.querySelectorAll('path.edge--reads_state.edge--inferred')).toHaveLength(1);
    expect(container.querySelectorAll('path.edge--uses_module')).toHaveLength(5);
    expect(container.querySelectorAll('.node--module polygon')).toHaveLength(3);
    expect(container.querySelectorAll('.node--stack rect.node__shape')).toHaveLength(5);
    expect(container.querySelectorAll('.node__wave')).toHaveLength(0);
  });

  it('activates nodes on click, Enter and Space but not other keys', () => {
    const onActivate = vi.fn();
    render(<Graph layout={layoutGraph(graph)} label="g" onActivate={onActivate} />);
    const eks = screen.getByRole('link', { name: 'Stack stacks/prod/eks' });
    fireEvent.click(eks);
    fireEvent.keyDown(eks, { key: 'Enter' });
    fireEvent.keyDown(eks, { key: ' ' });
    fireEvent.keyDown(eks, { key: 'a' });
    expect(onActivate).toHaveBeenCalledTimes(3);
    expect(onActivate.mock.calls.every(([n]) => (n as { key: string }).key === 'stacks/prod/eks')).toBe(true);
  });

  it('labels waves when replaying a run', () => {
    const { container } = render(<Graph layout={layoutGraph(graphRun, run)} label="g" onActivate={vi.fn()} />);
    expect([...container.querySelectorAll('.node__wave text')].map((t) => t.textContent).sort()).toEqual([
      'W0',
      'W0',
      'W1',
      'W1',
      'W2',
    ]);
  });

  it('zooms with the wheel and the toolbar, and resets', () => {
    render(<Graph layout={layoutGraph(graph)} label="g" onActivate={vi.fn()} />);
    const zoom = () => screen.getByText(/%$/).textContent;
    expect(zoom()).toBe('100%');
    fireEvent.click(screen.getByRole('button', { name: 'Zoom in' }));
    expect(zoom()).toBe('125%');
    fireEvent.click(screen.getByRole('button', { name: 'Zoom out' }));
    expect(zoom()).toBe('100%');
    fireEvent.wheel(screen.getByRole('group', { name: 'g' }), { deltaY: -200 });
    expect(zoom()).toBe('135%');
    fireEvent.click(screen.getByRole('button', { name: 'Reset view' }));
    expect(zoom()).toBe('100%');
  });
});

describe('GraphLegend', () => {
  it('explains shapes and lines, and statuses only when replaying', () => {
    const { rerender } = render(<GraphLegend replay={false} />);
    for (const text of ['Stack', 'Module', 'depends on', 'reads state (inferred)', 'uses module']) {
      expect(screen.getByText(text)).toBeInTheDocument();
    }
    expect(screen.queryByText('not affected')).not.toBeInTheDocument();
    rerender(<GraphLegend replay />);
    for (const text of ['apply wave', 'planned', 'applied', 'failed', 'blocked', 'not affected']) {
      expect(screen.getByText(text)).toBeInTheDocument();
    }
  });
});
