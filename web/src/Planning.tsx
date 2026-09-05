import { useQuery } from "@tanstack/react-query";
import { useState } from "react";
import { Compass, Sparkles } from "lucide-react";
import { api } from "./api";
import { timeAgo } from "./format";
import { LiveBadge } from "./Roadmap";
import type { CheckpointStatus, RoadmapCheckpoint, RoadmapPass, RoadmapProject } from "./types";
import { EmptyState, ErrorState, LoadingState, ViewHeader } from "./ui";

// The seven stops a checkpoint makes between an idea and a set of tasks. This
// page is the picture of that journey, so the stops are named once here and
// everything else on the page is placed against them.
const stages = ["Route", "Draft", "Critique", "Revise", "Review", "Freeze", "Pebbles"] as const;

const money = (value: number) => `$${value.toFixed(2)}`;

// A checkpoint being planned, lifted out of its project so the page can show
// them side by side. The project is carried along because the whole point of
// this page is looking across projects at once.
interface Planning {
  project: string;
  projectTitle: string;
  checkpoint: RoadmapCheckpoint;
  passes: RoadmapPass[];
  stage: number;
  live: boolean;
  stalled: boolean;
}

// A checkpoint is being planned from the moment drafting starts until its
// pebbles exist. A frozen checkpoint that already has pebbles is finished
// planning even though nothing has built it yet: that is the Roadmap's story,
// not this page's.
function planningOf(projects: RoadmapProject[]): Planning[] {
  const rows: Planning[] = [];
  for (const project of projects) {
    for (const checkpoint of project.checkpoints ?? []) {
      const pebbles = checkpoint.pebbles ?? [];
      const passes = checkpoint.passes ?? [];
      if (checkpoint.status === "planned" || checkpoint.status === "built") continue;
      if (checkpoint.status === "frozen" && pebbles.length > 0) continue;
      rows.push({
        project: project.project,
        projectTitle: project.title,
        checkpoint,
        passes,
        stage: stageOf(checkpoint.status, passes),
        live: checkpoint.status === "drafting",
        stalled: checkpoint.status === "fog",
      });
    }
  }
  // The ones actually turning come first, then the ones stuck, then the rest.
  const rank = (row: Planning) => (row.live ? 0 : row.stalled ? 1 : 2);
  return rows.sort((a, b) => rank(a) - rank(b) || a.project.localeCompare(b.project) || a.checkpoint.number - b.checkpoint.number);
}

// Where on the rail a checkpoint is standing. While it is drafting the last
// pass that ran is the truthful answer, because draft, critique and revise are
// three different stops and the status word cannot tell them apart.
function stageOf(status: CheckpointStatus, passes: RoadmapPass[]): number {
  if (status === "review") return 4;
  if (status === "frozen") return 5;
  if (status === "fog") return 1;
  const last = passes[passes.length - 1];
  const byMode: Record<string, number> = { route: 0, draft: 1, critique: 2, revise: 3, freeze: 5, pebble: 6 };
  return last ? byMode[last.mode] ?? 1 : 1;
}

export function PlanningView({ onProject }: { onProject: (project: string, checkpoint: number) => void }) {
  const query = useQuery({ queryKey: ["roadmap"], queryFn: api.roadmap, refetchInterval: 10_000 });
  const [focus, setFocus] = useState<string | null>(null);
  if (query.isPending) return <LoadingState label="Loading Planning" />;
  if (query.isError) return <ErrorState error={query.error} onRetry={() => void query.refetch()} />;
  const roadmap = query.data;
  const header = <ViewHeader title="Planning" fetching={query.isFetching} updatedAt={query.dataUpdatedAt} onRefresh={() => void query.refetch()} />;
  if (!roadmap.configured) {
    return <div className="page">{header}<EmptyState icon={<Compass size={22} />} title="No roadmap is configured" description="Point the server at the orchestrator's state directory with roadmap_root and planning appears here." /></div>;
  }
  const rows = planningOf(roadmap.projects);
  if (!rows.length) {
    return <div className="page">{header}
      <EmptyState icon={<Sparkles size={22} />} title="Nothing is being planned" description="Every checkpoint is either already split into pebbles or still just a line on a route. Planning shows up here the moment a pass starts." />
      <PlanningRail stage={-1} />
    </div>;
  }
  const key = (row: Planning) => `${row.project}:${row.checkpoint.number}`;
  const active = rows.find((row) => key(row) === focus) ?? rows[0];
  const liveCount = rows.filter((row) => row.live).length;
  return <div className="page planning-page">
    {header}
    <div className="view-toolbar">
      <p>What is being planned right now, and how far through the pass it is. The Roadmap shows what planning has already produced.</p>
      {liveCount > 0 && <LiveBadge label={liveCount === 1 ? "1 agent writing" : `${liveCount} agents writing`} />}
    </div>
    <PlanningStage row={active} onOpen={() => onProject(active.project, active.checkpoint.number)} />
    {rows.length > 1 && <div className="planning-others">
      {rows.map((row) => <button
        key={key(row)}
        className={`planning-chip ${key(row) === key(active) ? "active" : ""} ${row.live ? "live" : ""} ${row.stalled ? "stalled" : ""}`}
        aria-current={key(row) === key(active) ? "true" : undefined}
        onClick={() => setFocus(key(row))}
      >
        <span className="planning-chip-project mono">{row.project}</span>
        <span className="planning-chip-title">{row.checkpoint.number}. {row.checkpoint.title}</span>
        <span className="planning-chip-stage">{stages[row.stage]}</span>
      </button>)}
    </div>}
  </div>;
}

// The one being watched, at full size: the rail it is standing on, the picture
// of the passes it has played, and the money it has cost so far.
function PlanningStage({ row, onOpen }: { row: Planning; onOpen: () => void }) {
  const { checkpoint, passes } = row;
  return <section className="planning-stage">
    <header className="planning-stage-head">
      <div>
        <span className="eyebrow mono">{row.project} · checkpoint {checkpoint.number}</span>
        <h2>{checkpoint.title}</h2>
        {checkpoint.summary && <p className="planning-stage-summary">{checkpoint.summary}</p>}
      </div>
      <div className="planning-stage-meta">
        {row.live && <LiveBadge label="Agent writing" />}
        {row.stalled && <span className="planning-stalled">Stuck on questions</span>}
        <span className="mono">{money(checkpoint.cost_usd)} · {checkpoint.pass_rounds} {checkpoint.pass_rounds === 1 ? "pass" : "passes"}</span>
        <button className="button button-secondary" onClick={onOpen}>Open on the roadmap</button>
      </div>
    </header>
    <PlanningRail stage={row.stage} live={row.live} />
    {row.live && <PlanningOrbit passes={passes} />}
    {passes.length > 0 ? <PlanningRally passes={passes} /> : <p className="quiet-empty">No pass has run for this checkpoint yet.</p>}
  </section>;
}

// The rail: every stop, with the current one lit. A live checkpoint pulses on
// its stop, so the page answers "is anything happening" without being read.
function PlanningRail({ stage, live }: { stage: number; live?: boolean }) {
  return <ol className="planning-rail" aria-label="Planning stages">
    {stages.map((label, index) => <li
      key={label}
      className={`rail-stop ${index < stage ? "done" : ""} ${index === stage ? "here" : ""} ${index === stage && live ? "live" : ""}`}
      aria-current={index === stage ? "step" : undefined}
    >
      <span className="rail-dot" aria-hidden="true" />
      <span className="rail-label">{label}</span>
    </li>)}
  </ol>;
}

// Agents turning. It says nothing about how far along the work is, because
// nothing on disk knows that until the pass writes its result.
function PlanningOrbit({ passes }: { passes: RoadmapPass[] }) {
  const latest = passes[passes.length - 1];
  return <div className="planning-orbit">
    <svg viewBox="0 0 260 200" role="img" aria-label="Planning agents are running">
      <circle className="orbit-ring" cx="130" cy="100" r="70" />
      <g className="orbit-spin">
        <circle className="orbit-beam" cx="130" cy="30" r="7" />
      </g>
      <circle className="orbit-core" cx="130" cy="100" r="30" />
      <text className="orbit-core-label" x="130" y="104" textAnchor="middle">plan</text>
      {[["Draft", 130, 24], ["Critique", 194, 138], ["Revise", 66, 138]].map(([label, x, y]) => <g key={label as string}>
        <circle className="orbit-node" cx={x as number} cy={y as number} r="6" />
        <text className="orbit-node-label" x={x as number} y={(y as number) + (label === "Draft" ? -14 : 22)} textAnchor="middle">{label}</text>
      </g>)}
    </svg>
    <div className="planning-live">
      <LiveBadge label="Agent working" />
      {latest && <span className="mono">last pass {latest.mode}, round {latest.round}, {timeAgo(latest.at)}</span>}
    </div>
  </div>;
}

// The rally: every pass as a hop between the two sides of the table. A pass
// that critiqued sits on the lower lane, everything else on the upper one, so
// the shape of the exchange is the shape of the picture.
function PlanningRally({ passes }: { passes: RoadmapPass[] }) {
  const step = 140;
  const width = Math.max(420, passes.length * step + 80);
  const point = (index: number) => ({ x: 50 + index * step, y: passes[index].mode === "critique" ? 168 : 62 });
  const path = passes.map((_, index) => {
    const here = point(index);
    if (index === 0) return `M ${here.x} ${here.y}`;
    const previous = point(index - 1);
    return `Q ${(previous.x + here.x) / 2} ${(previous.y + here.y) / 2 + (here.y > previous.y ? 52 : -52)} ${here.x} ${here.y}`;
  }).join(" ");
  return <div className="planning-rally">
    <svg viewBox={`0 0 ${width} 240`} width={width} height="240" role="img" aria-label={`${passes.length} planning passes`}>
      <line className="rally-lane" x1="24" y1="62" x2={width - 24} y2="62" />
      <line className="rally-lane" x1="24" y1="168" x2={width - 24} y2="168" />
      <text className="rally-lane-label" x="24" y="40">write</text>
      <text className="rally-lane-label" x="24" y="208">critique</text>
      <path className="rally-path" d={path} />
      {passes.map((pass, index) => {
        const here = point(index);
        return <g key={`${pass.at}-${index}`}>
          <circle className={`rally-node rally-${pass.outcome === "ok" ? "ok" : "other"}`} cx={here.x} cy={here.y} r="11" />
          <text className="rally-mode" x={here.x} y={here.y - 22} textAnchor="middle">{pass.mode}</text>
          <text className="rally-cost" x={here.x} y={here.y + 30} textAnchor="middle">{money(pass.cost_usd)}</text>
        </g>;
      })}
    </svg>
  </div>;
}
