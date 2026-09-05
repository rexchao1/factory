import { useQuery } from "@tanstack/react-query";
import { useState } from "react";
import { ArrowLeft, ChevronRight, CircleCheck, Inbox, Map as MapIcon } from "lucide-react";
import { api } from "./api";
import { timeAgo } from "./format";
import type { CheckpointStatus, LoadedRoadmap, RoadmapBoulder, RoadmapCheckpoint, RoadmapPass } from "./types";
import { EmptyState, ErrorState, LoadingState, ViewHeader } from "./ui";

export type RoadmapTab = "plan" | "planning";

// A checkpoint reads as one of six words. The cockpit's own state palette has
// no vocabulary for planning, so these get their own, and the order here is
// the order a checkpoint moves through.
const statusLabels: Record<CheckpointStatus, string> = {
  planned: "Planned",
  drafting: "Drafting",
  review: "Waiting on you",
  fog: "Stuck on questions",
  frozen: "Frozen",
  built: "Built",
};

// drafting is the one status that means an agent is running right now. Every
// surface that can show a checkpoint shows that differently, because it is the
// single thing the human asked to be able to see at a glance.
const isLive = (status: CheckpointStatus) => status === "drafting";

const money = (value: number) => `$${value.toFixed(2)}`;

const checkpointsOf = (boulder: RoadmapBoulder) => boulder.checkpoints ?? [];

export function RoadmapView({ project, checkpoint, tab, onBoulder, onView, onWaiting }: {
  project?: string;
  checkpoint?: number;
  tab: RoadmapTab;
  onBoulder: (project: string | null) => void;
  // onView always names both halves of the selection. The panel resolves a
  // missing checkpoint to a sensible default, and a link that only said "the
  // planning tab" would lose that resolution the moment it was shared.
  onView: (checkpoint: number, tab: RoadmapTab) => void;
  onWaiting: () => void;
}) {
  const query = useQuery({ queryKey: ["roadmap"], queryFn: api.roadmap, refetchInterval: 15_000 });
  if (query.isPending) return <LoadingState label="Loading Roadmap" />;
  if (query.isError) return <ErrorState error={query.error} onRetry={() => void query.refetch()} />;
  const roadmap = query.data;
  const header = <ViewHeader title="Roadmap" fetching={query.isFetching} updatedAt={query.dataUpdatedAt} onRefresh={() => void query.refetch()} />;
  if (!roadmap.configured) return <div className="page">{header}<UnconfiguredRoadmap /></div>;
  const selected = project ? roadmap.boulders.find((boulder) => boulder.project === project) : undefined;
  if (project && !selected) {
    return <div className="page">{header}<EmptyState icon={<MapIcon size={22} />} title="No boulder called that" description={`Nothing on the roadmap is planning ${project}.`} action={<button className="button button-secondary" onClick={() => onBoulder(null)}>Back to the roadmap</button>} /></div>;
  }
  return <div className="page roadmap-page">
    {header}
    {selected
      ? <BoulderDetail boulder={selected} checkpoint={checkpoint} tab={tab} onBack={() => onBoulder(null)} onView={onView} />
      : <BoulderGrid roadmap={roadmap} onBoulder={onBoulder} onWaiting={onWaiting} />}
  </div>;
}

function UnconfiguredRoadmap() {
  return <EmptyState
    icon={<MapIcon size={22} />}
    title="No roadmap is configured"
    description="Point the server at the orchestrator's state directory with roadmap_root, or -roadmap-root, and its boulders, checkpoints, and pebbles appear here. The factory only reads those files."
  />;
}

// The landing is one card per boulder, not a list of every checkpoint under
// every boulder. Checkpoints and pebbles keep arriving, and a page that grows
// with them stops being readable long before it stops fitting.
function BoulderGrid({ roadmap, onBoulder, onWaiting }: { roadmap: LoadedRoadmap; onBoulder: (project: string) => void; onWaiting: () => void }) {
  const waiting = roadmap.waiting;
  if (!roadmap.boulders.length) {
    return <EmptyState icon={<MapIcon size={22} />} title="Nothing planned yet" description="A boulder appears here once the orchestrator writes its route." />;
  }
  return <>
    <div className="view-toolbar">
      <p>Every boulder being planned, and how far along its route it is.</p>
      {waiting.length > 0 && <button className="button button-secondary" onClick={onWaiting}><Inbox size={15} /> {waiting.length} waiting on you</button>}
    </div>
    <div className="boulder-grid">{roadmap.boulders.map((boulder) => {
      const checkpoints = checkpointsOf(boulder);
      const live = checkpoints.filter((entry) => isLive(entry.status)).length;
      const needsYou = waiting.filter((entry) => entry.project === boulder.project).length;
      return <button className="boulder-card" key={boulder.project} onClick={() => onBoulder(boulder.project)}>
        <span className="boulder-card-head">
          <span className="boulder-id">{boulder.id}</span>
          <strong>{boulder.title}</strong>
          <ChevronRight size={16} className="boulder-card-chevron" />
        </span>
        {boulder.statement && <span className="boulder-statement">{boulder.statement}</span>}
        <span className="boulder-chain" aria-label={`${checkpoints.length} checkpoints, ${boulder.built_count} built`}>
          {checkpoints.map((entry) => <i key={entry.number} className={`chain-pip status-plan-${entry.status}`} title={`${entry.number}. ${entry.title}`} />)}
        </span>
        <span className="boulder-card-foot">
          <span>{boulder.built_count} of {checkpoints.length} built</span>
          <span className="mono">{money(boulder.cost_usd)} planned</span>
          {live > 0 && <LiveBadge label={live === 1 ? "Agent working" : `${live} agents working`} />}
          {needsYou > 0 && <span className="waiting-pill">{needsYou} waiting on you</span>}
        </span>
      </button>;
    })}</div>
    <Vocabulary />
  </>;
}

// The four words this page uses, said once, in the order they nest. The rows
// themselves stay plainly numbered.
function Vocabulary() {
  return <dl className="vocabulary">
    <div><dt>B1</dt><dd>boulder, the thing being built</dd></div>
    <div><dt>B1.2</dt><dd>checkpoint, one rung of its route</dd></div>
    <div><dt>B1.2.3</dt><dd>pebble, one task the factory can take</dd></div>
    <div><dt>B1.2.3.2</dt><dd>work, one run against that pebble</dd></div>
  </dl>;
}

function BoulderDetail({ boulder, checkpoint, tab, onBack, onView }: {
  boulder: RoadmapBoulder;
  checkpoint?: number;
  tab: RoadmapTab;
  onBack: () => void;
  onView: (checkpoint: number, tab: RoadmapTab) => void;
}) {
  const checkpoints = checkpointsOf(boulder);
  const active = checkpoints.find((entry) => entry.number === checkpoint) ?? defaultCheckpoint(checkpoints);
  return <>
    <button className="back-link" onClick={onBack}><ArrowLeft size={15} /> All boulders</button>
    <header className="boulder-heading">
      <span className="boulder-id">{boulder.id}</span>
      <div className="boulder-heading-body">
        <h2>{boulder.title}</h2>
        {boulder.statement && <Statement text={boulder.statement} />}
      </div>
      <span className="boulder-heading-meta mono">{boulder.built_count}/{checkpoints.length} built · {money(boulder.cost_usd)} planned</span>
    </header>
    {checkpoints.length === 0
      ? <div className="quiet-empty">This route has no checkpoints yet.</div>
      : <>
        <nav className="checkpoint-bar" aria-label="Checkpoints">
          {checkpoints.map((entry) => <button
            key={entry.number}
            className={`checkpoint-tab status-plan-${entry.status} ${entry.number === active?.number ? "active" : ""}`}
            aria-current={entry.number === active?.number ? "true" : undefined}
            onClick={() => onView(entry.number, tab)}
          >
            <span className="checkpoint-tab-number">{entry.status === "built" ? <CircleCheck size={13} /> : entry.number}</span>
            <span className="checkpoint-tab-title">{entry.title}</span>
            <span className="checkpoint-tab-status">{isLive(entry.status) ? <LiveBadge label="Working" /> : statusLabels[entry.status]}</span>
          </button>)}
        </nav>
        {active && <CheckpointPanel key={active.number} boulder={boulder} checkpoint={active} tab={tab} onTab={(next) => onView(active.number, next)} />}
      </>}
  </>;
}

// Opening a boulder should land on the checkpoint that is actually moving.
// The first unbuilt one is that checkpoint; a fully built route falls back to
// its last rung rather than to nothing.
function defaultCheckpoint(checkpoints: RoadmapCheckpoint[]): RoadmapCheckpoint | undefined {
  return checkpoints.find((entry) => entry.status !== "built") ?? checkpoints[checkpoints.length - 1];
}

// A boulder statement is the whole of what the human asked for, and it runs to
// paragraphs. It stays available in full, and stays out of the way until asked
// for, because the page above it is the thing being read.
function Statement({ text }: { text: string }) {
  const [open, setOpen] = useState(false);
  const long = text.length > 220;
  return <div className="boulder-statement-block">
    <p className={long && !open ? "clamped" : ""}>{text}</p>
    {long && <button className="statement-toggle" onClick={() => setOpen((current) => !current)}>{open ? "Show less" : "Show more"}</button>}
  </div>;
}

function CheckpointPanel({ boulder, checkpoint, tab, onTab }: { boulder: RoadmapBoulder; checkpoint: RoadmapCheckpoint; tab: RoadmapTab; onTab: (tab: RoadmapTab) => void }) {
  const pebbles = checkpoint.pebbles ?? [];
  const passes = checkpoint.passes ?? [];
  return <section className="panel checkpoint-panel" aria-label={`Checkpoint ${checkpoint.number}`}>
    <header className="checkpoint-head">
      <div>
        <span className="eyebrow">{boulder.id}.{checkpoint.number}</span>
        <h3>{checkpoint.title}</h3>
      </div>
      <span className={`plan-badge status-plan-${checkpoint.status}`}><span className="status-dot" />{statusLabels[checkpoint.status]}</span>
    </header>
    <div className="checkpoint-tabs" role="tablist" aria-label="Checkpoint view">
      <button role="tab" aria-selected={tab === "plan"} onClick={() => onTab("plan")}>Plan</button>
      <button role="tab" aria-selected={tab === "planning"} onClick={() => onTab("planning")}>Planning</button>
    </div>
    {tab === "plan"
      ? <div className="checkpoint-plan">
        {checkpoint.summary && <p className="checkpoint-summary">{checkpoint.summary}</p>}
        {!checkpoint.planned && <p className="quiet-empty">Still a line on the route. Nothing has been drafted for it yet.</p>}
        {pebbles.length > 0
          // The number shown is the pebble's position in the list, not the
          // digits its file happens to start with. Task files are numbered
          // from 00 in some checkpoints and from 01 in others, and a plan
          // whose first item is "0" reads as a mistake.
          ? <ol className="pebble-list">{pebbles.map((pebble, index) => <li key={pebble.slug}><span className="pebble-ordinal">{index + 1}</span><span>{pebble.title}</span></li>)}</ol>
          : checkpoint.planned && <p className="quiet-empty">{checkpoint.status === "frozen" ? "Frozen and ready to be split into pebbles." : "No pebbles yet. A checkpoint is split once it is frozen."}</p>}
        <p className="checkpoint-cost mono">{money(checkpoint.cost_usd)} across {checkpoint.pass_rounds} planning {checkpoint.pass_rounds === 1 ? "pass" : "passes"}</p>
      </div>
      : <PlanningView checkpoint={checkpoint} passes={passes} />}
  </section>;
}

// Two pictures, because a plan being written and a plan already written are
// different questions. A live one shows that agents are turning; a finished
// one shows the rally they actually played, one hop per pass, with what each
// hop cost.
function PlanningView({ checkpoint, passes }: { checkpoint: RoadmapCheckpoint; passes: RoadmapPass[] }) {
  if (isLive(checkpoint.status)) return <PlanningOrbit passes={passes} />;
  if (!passes.length) return <p className="quiet-empty">No planning passes have run for this checkpoint yet.</p>;
  return <PlanningRally passes={passes} />;
}

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
  const step = 118;
  const width = Math.max(360, passes.length * step + 60);
  const point = (index: number) => ({ x: 40 + index * step, y: passes[index].mode === "critique" ? 132 : 52 });
  const path = passes.map((_, index) => {
    const here = point(index);
    if (index === 0) return `M ${here.x} ${here.y}`;
    const previous = point(index - 1);
    return `Q ${(previous.x + here.x) / 2} ${(previous.y + here.y) / 2 + (here.y > previous.y ? 42 : -42)} ${here.x} ${here.y}`;
  }).join(" ");
  return <div className="planning-rally">
    <svg viewBox={`0 0 ${width} 200`} width={width} height="200" role="img" aria-label={`${passes.length} planning passes`}>
      <line className="rally-lane" x1="20" y1="52" x2={width - 20} y2="52" />
      <line className="rally-lane" x1="20" y1="132" x2={width - 20} y2="132" />
      <text className="rally-lane-label" x="20" y="34">write</text>
      <text className="rally-lane-label" x="20" y="172">critique</text>
      <path className="rally-path" d={path} />
      {passes.map((pass, index) => {
        const here = point(index);
        return <g key={`${pass.at}-${index}`}>
          <circle className={`rally-node rally-${pass.outcome === "ok" ? "ok" : "other"}`} cx={here.x} cy={here.y} r="9" />
          <text className="rally-mode" x={here.x} y={here.y - 18} textAnchor="middle">{pass.mode}</text>
          <text className="rally-cost" x={here.x} y={here.y + 26} textAnchor="middle">{money(pass.cost_usd)}</text>
        </g>;
      })}
    </svg>
  </div>;
}

function LiveBadge({ label }: { label: string }) {
  return <span className="live-badge"><span className="live-dot" aria-hidden="true" />{label}</span>;
}

// Waiting is its own page because it answers one question, and answering it
// should never mean opening a boulder to find out.
export function WaitingView({ onBoulder }: { onBoulder: (project: string, checkpoint: number) => void }) {
  const query = useQuery({ queryKey: ["roadmap"], queryFn: api.roadmap, refetchInterval: 15_000 });
  if (query.isPending) return <LoadingState label="Loading what is waiting" />;
  if (query.isError) return <ErrorState error={query.error} onRetry={() => void query.refetch()} />;
  const roadmap = query.data;
  return <div className="page">
    <ViewHeader title="Waiting for you" fetching={query.isFetching} updatedAt={query.dataUpdatedAt} onRefresh={() => void query.refetch()} />
    {!roadmap.configured ? <UnconfiguredRoadmap />
      : !roadmap.waiting.length
        ? <EmptyState icon={<CircleCheck size={22} />} title="Nothing is waiting on you" description="Every checkpoint being planned is either moving on its own or already built." />
        : <div className="waiting-list">{roadmap.waiting.map((entry) => <button className="waiting-card" key={`${entry.project}-${entry.number}`} onClick={() => onBoulder(entry.project, entry.number)}>
          <span className="waiting-card-head">
            <span className="boulder-id">{entry.boulder}.{entry.number}</span>
            <strong>{entry.title}</strong>
            <span className={`plan-badge status-plan-${entry.status}`}><span className="status-dot" />{statusLabels[entry.status]}</span>
          </span>
          <span className="waiting-reason">{entry.reason}</span>
          <span className="waiting-card-foot">
            <span className="waiting-action">{entry.action}<ChevronRight size={14} /></span>
            <span className="mono">{entry.project} · {money(entry.cost_usd)} planned</span>
          </span>
        </button>)}</div>}
  </div>;
}
