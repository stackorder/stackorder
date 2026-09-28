import type { JSX, TargetedKeyboardEvent, TargetedPointerEvent, TargetedWheelEvent } from 'preact';
import { useEffect, useRef, useState } from 'preact/hooks';

import { humanize } from '../format';
import { edgePath, hexagonPoints, type GraphLayout, type LayoutNode } from '../graph/layout';
import { statusTone } from './StatusBadge';

interface View {
  x: number;
  y: number;
  k: number;
}

interface Drag {
  pointer: number;
  clientX: number;
  clientY: number;
  start: View;
}

type MaybeScreenCTM = Partial<Pick<SVGGraphicsElement, 'getScreenCTM'>>;

function screenMatrix(svg: SVGSVGElement | null): DOMMatrix | null {
  return svg ? ((svg as MaybeScreenCTM).getScreenCTM?.() ?? null) : null;
}

const MIN_ZOOM = 0.25;
const MAX_ZOOM = 4;
const IDENTITY: View = { x: 0, y: 0, k: 1 };
const EDGE_TYPES = ['depends_on', 'reads_state', 'uses_module'] as const;

function clamp(k: number): number {
  return Math.min(MAX_ZOOM, Math.max(MIN_ZOOM, k));
}

/** Zooms a view by factor around a point in graph coordinates. */
export function zoomAround(view: View, factor: number, px: number, py: number): View {
  const k = clamp(view.k * factor);
  const ratio = k / view.k;
  return { k, x: px - (px - view.x) * ratio, y: py - (py - view.y) * ratio };
}

/** CSS classes of a node: its kind, replay state and status tone. */
export function nodeClass(node: LayoutNode, replay: boolean): string {
  const classes = ['node', `node--${node.kind}`];
  if (node.moduleKind) classes.push(`node--${node.moduleKind}`);
  if (node.external) classes.push('node--external');
  if (replay) classes.push(node.affected ? 'node--affected' : 'node--dimmed');
  if (node.status) classes.push(`node--tone-${statusTone(node.status)}`);
  return classes.join(' ');
}

/** The accessible name of a node, spelling out what the colours and badges show. */
export function nodeLabel(node: LayoutNode): string {
  const parts = [`${node.kind === 'stack' ? 'Stack' : 'Module'} ${node.key}`];
  if (node.status) parts.push(`status ${humanize(node.status)}`);
  if (node.wave !== undefined) parts.push(`wave ${String(node.wave)}`);
  if (node.reasons.length) parts.push(`affected because ${node.reasons.map(humanize).join(', ')}`);
  else if (node.affected && node.kind === 'module') parts.push('on the change path');
  if (node.external) parts.push('in another repository');
  return parts.join(', ');
}

/** Props of Graph. */
export interface GraphProps {
  layout: GraphLayout;
  label: string;
  onActivate: (node: LayoutNode) => void;
}

/** The dependency graph as SVG with wheel zoom, drag pan and keyboard-reachable nodes. */
export function Graph({ layout, label, onActivate }: GraphProps) {
  const svgRef = useRef<SVGSVGElement>(null);
  const drag = useRef<Drag | null>(null);
  const [view, setView] = useState<View>(IDENTITY);

  useEffect(() => {
    setView(IDENTITY);
  }, [layout]);

  const scale = (): number => screenMatrix(svgRef.current)?.a ?? 1;

  const toGraph = (clientX: number, clientY: number): { x: number; y: number } => {
    const ctm = screenMatrix(svgRef.current);
    if (!ctm) return { x: layout.width / 2, y: layout.height / 2 };
    const p = new DOMPoint(clientX, clientY).matrixTransform(ctm.inverse());
    return { x: p.x, y: p.y };
  };

  const onWheel = (e: TargetedWheelEvent<SVGSVGElement>) => {
    e.preventDefault();
    const p = toGraph(e.clientX, e.clientY);
    const factor = Math.exp(-e.deltaY * 0.0015);
    setView((v) => zoomAround(v, factor, p.x, p.y));
  };

  const onPointerDown = (e: TargetedPointerEvent<SVGSVGElement>) => {
    if (e.button !== 0 || (e.target as Element).closest('.node')) return;
    drag.current = { pointer: e.pointerId, clientX: e.clientX, clientY: e.clientY, start: view };
    e.currentTarget.setPointerCapture(e.pointerId);
    e.currentTarget.classList.add('graph__svg--dragging');
  };

  const onPointerMove = (e: TargetedPointerEvent<SVGSVGElement>) => {
    const d = drag.current;
    if (d?.pointer !== e.pointerId) return;
    const s = scale();
    setView({ ...d.start, x: d.start.x + (e.clientX - d.clientX) / s, y: d.start.y + (e.clientY - d.clientY) / s });
  };

  const onPointerUp = (e: TargetedPointerEvent<SVGSVGElement>) => {
    if (drag.current?.pointer !== e.pointerId) return;
    drag.current = null;
    if (e.currentTarget.hasPointerCapture(e.pointerId)) e.currentTarget.releasePointerCapture(e.pointerId);
    e.currentTarget.classList.remove('graph__svg--dragging');
  };

  const zoomCentre = (factor: number) => {
    setView((v) => zoomAround(v, factor, layout.width / 2, layout.height / 2));
  };

  const onNodeKey = (node: LayoutNode) => (e: TargetedKeyboardEvent<SVGGElement>) => {
    if (e.key === 'Enter' || e.key === ' ') {
      e.preventDefault();
      onActivate(node);
    }
  };

  const transform = `translate(${String(view.x)},${String(view.y)}) scale(${String(view.k)})`;

  return (
    <div class="graph">
      <div class="graph__toolbar" role="toolbar" aria-label="Graph view">
        <button type="button" class="button button--small" onClick={() => { zoomCentre(1.25); }} aria-label="Zoom in">
          +
        </button>
        <button type="button" class="button button--small" onClick={() => { zoomCentre(0.8); }} aria-label="Zoom out">
          −
        </button>
        <button type="button" class="button button--small" onClick={() => { setView(IDENTITY); }}>
          Reset view
        </button>
        <span class="graph__zoom" aria-live="polite">
          {Math.round(view.k * 100)}%
        </span>
      </div>
      <svg
        ref={svgRef}
        class="graph__svg"
        viewBox={`0 0 ${String(layout.width)} ${String(layout.height)}`}
        style={{ aspectRatio: `${String(layout.width)} / ${String(layout.height)}` }}
        role="group"
        aria-label={label}
        onWheel={onWheel}
        onPointerDown={onPointerDown}
        onPointerMove={onPointerMove}
        onPointerUp={onPointerUp}
        onPointerCancel={onPointerUp}
      >
        <defs>
          {[...EDGE_TYPES, 'affected'].map((t) => (
            <marker
              key={t}
              id={`so-arrow-${t}`}
              class={`arrow arrow--${t}`}
              viewBox="0 0 10 10"
              refX="9"
              refY="5"
              markerWidth="7"
              markerHeight="7"
              orient="auto-start-reverse"
            >
              <path d="M0,0 L10,5 L0,10 z" />
            </marker>
          ))}
        </defs>
        <g class="graph__viewport" transform={transform}>
          <g class="graph__edges">
            {layout.edges.map((e) => (
              <path key={e.id} class={e.className} d={edgePath(e.points)} data-type={e.type}>
                <title>
                  {humanize(e.type)}
                  {e.inferred ? ' (inferred)' : ''}
                  {e.ref ? ` @ ${e.ref}` : ''}
                </title>
              </path>
            ))}
          </g>
          <g class="graph__nodes">
            {layout.nodes.map((n) => (
              <g
                key={n.id}
                class={nodeClass(n, layout.replay)}
                transform={`translate(${String(n.x - n.width / 2)},${String(n.y - n.height / 2)})`}
                tabindex={0}
                role="link"
                aria-label={nodeLabel(n)}
                data-key={n.key}
                onClick={() => {
                  onActivate(n);
                }}
                onKeyDown={onNodeKey(n)}
              >
                <title>{n.key}</title>
                {n.kind === 'stack' ? (
                  <rect class="node__shape" width={n.width} height={n.height} rx={10} ry={10} />
                ) : (
                  <polygon class="node__shape" points={hexagonPoints(n.width, n.height)} />
                )}
                <text class="node__label" x={n.width / 2} y={n.height / 2 - 3}>
                  {n.label}
                </text>
                <text class="node__detail" x={n.width / 2} y={n.height / 2 + 14}>
                  {n.detail}
                </text>
                {n.wave !== undefined && (
                  <g class="node__wave" transform={`translate(${String(n.width - 16)},-8)`} aria-hidden="true">
                    <rect x={-14} y={0} width={30} height={17} rx={8.5} ry={8.5} />
                    <text x={1} y={12.5}>
                      W{n.wave}
                    </text>
                  </g>
                )}
              </g>
            ))}
          </g>
        </g>
      </svg>
    </div>
  );
}

function LegendSwatch({ children }: { children: JSX.Element }) {
  return (
    <svg class="legend__swatch" viewBox="0 0 40 20" width="40" height="20" aria-hidden="true">
      {children}
    </svg>
  );
}

/** Explains the graph's shapes, line styles and replay colours. */
export function GraphLegend({ replay }: { replay: boolean }) {
  return (
    <div class="legend">
      <h2 class="legend__title">Legend</h2>
      <ul class="legend__list">
        <li>
          <LegendSwatch>
            <rect class="legend__stack" x="2" y="3" width="36" height="14" rx="4" />
          </LegendSwatch>
          Stack
        </li>
        <li>
          <LegendSwatch>
            <polygon class="legend__module" points="8,3 32,3 38,10 32,17 8,17 2,10" />
          </LegendSwatch>
          Module
        </li>
        <li>
          <LegendSwatch>
            <line class="edge edge--depends_on" x1="2" y1="10" x2="38" y2="10" />
          </LegendSwatch>
          depends on
        </li>
        <li>
          <LegendSwatch>
            <line class="edge edge--reads_state edge--inferred" x1="2" y1="10" x2="38" y2="10" />
          </LegendSwatch>
          reads state (inferred)
        </li>
        <li>
          <LegendSwatch>
            <line class="edge edge--uses_module" x1="2" y1="10" x2="38" y2="10" />
          </LegendSwatch>
          uses module
        </li>
        {replay && (
          <>
            <li>
              <span class="legend__wave" aria-hidden="true">
                W0
              </span>
              apply wave
            </li>
            {(['planned', 'applied', 'failed', 'blocked'] as const).map((s) => (
              <li key={s}>
                <LegendSwatch>
                  <rect class={`legend__status node--tone-${statusTone(s)}`} x="2" y="3" width="36" height="14" rx="4" />
                </LegendSwatch>
                {s}
              </li>
            ))}
            <li>
              <LegendSwatch>
                <rect class="legend__stack legend__dimmed" x="2" y="3" width="36" height="14" rx="4" />
              </LegendSwatch>
              not affected
            </li>
          </>
        )}
      </ul>
      <p class="legend__note">Arrows point from a dependency to what depends on it, the order changes flow and applies run.</p>
    </div>
  );
}
