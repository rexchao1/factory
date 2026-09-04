import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { ArrowDown, GitMerge, GripVertical, Pencil, Plus, Trash2, X } from "lucide-react";
import { useState } from "react";
import { api } from "./api";
import type { Pipeline, PipelineStageKind } from "./types";
import { EmptyState, ErrorState, InlineError, LoadingState, ViewHeader } from "./ui";

type DraftStage = { name: string; kind: PipelineStageKind; prompt: string; command: string; model: string; effort: string };

const newAgentStage = (name = "Do the task", prompt = "{{ task.prompt }}"): DraftStage =>
  ({ name, kind: "agent", prompt, command: "", model: "", effort: "" });

const stageInput = (stage: DraftStage) => {
  if (stage.kind === "delivery") return { name: stage.name, kind: stage.kind };
  if (stage.kind === "code") return { name: stage.name, kind: stage.kind, command: stage.command };
  return {
    name: stage.name, prompt: stage.prompt,
    ...(stage.model ? { model: stage.model } : {}),
    ...(stage.effort ? { effort: stage.effort } : {}),
  };
};

export function PipelinesView() {
  const client = useQueryClient();
  const query = useQuery({ queryKey: ["pipelines"], queryFn: api.pipelines });
  const [editing, setEditing] = useState<string | "new" | null>(null);
  const selected = useQuery({ queryKey: ["pipeline", editing], queryFn: () => api.pipeline(editing!), enabled: editing !== null && editing !== "new" });
  const saved = (pipeline: Pipeline) => {
    client.setQueryData(["pipeline", pipeline.id], pipeline);
    setEditing(null);
    void client.invalidateQueries({ queryKey: ["pipelines"] });
  };
  if (query.isPending) return <LoadingState label="Loading Pipelines" />;
  if (query.isError) return <ErrorState error={query.error} onRetry={() => void query.refetch()} />;
  return <div className="page pipeline-page">
    <ViewHeader title="Pipelines" fetching={query.isFetching} updatedAt={query.dataUpdatedAt} onRefresh={() => void query.refetch()} />
    <div className="view-toolbar"><p>Reusable sequences of agent, check, and delivery stages. A Task is the input.</p><button className="button button-primary" onClick={() => setEditing("new")}><Plus size={15} /> New Pipeline</button></div>
    {!query.data.length ? <EmptyState icon={<GitMerge size={22} />} title="No Pipelines yet" description="Create a sequence with one or more execution stages." action={<button className="button button-primary" onClick={() => setEditing("new")}><Plus size={15} /> New Pipeline</button>} /> :
      <div className="pipeline-grid">{query.data.map((pipeline) => <button className="pipeline-card" key={pipeline.id} onClick={() => setEditing(pipeline.id)}>
        <span className="pipeline-card-head"><strong>{pipeline.name}</strong><Pencil size={14} /></span>
        <span className="pipeline-mini-graph">{pipeline.stages.map((stage, index) => <span key={stage.position}><i>{index + 1}</i><b>{stage.name}</b>{index < pipeline.stages.length - 1 && <ArrowDown size={13} />}</span>)}</span>
        <small>{pipeline.stages.filter((stage) => !stage.kind || stage.kind === "agent").length} model start{pipeline.stages.filter((stage) => !stage.kind || stage.kind === "agent").length === 1 ? "" : "s"} per repository</small>
      </button>)}</div>}
    {selected.error && <InlineError error={selected.error} />}
    {editing === "new" && <PipelineEditor onClose={() => setEditing(null)} onSaved={saved} />}
    {editing !== null && editing !== "new" && selected.data && <PipelineEditor pipeline={selected.data} onClose={() => setEditing(null)} onSaved={saved} onDeleted={() => { client.removeQueries({ queryKey: ["pipeline", selected.data.id] }); setEditing(null); void client.invalidateQueries({ queryKey: ["pipelines"] }); }} />}
  </div>;
}

function PipelineEditor({ pipeline, onClose, onSaved, onDeleted }: { pipeline?: Pipeline; onClose: () => void; onSaved: (saved: Pipeline) => void; onDeleted?: () => void }) {
  const [name, setName] = useState(pipeline?.name ?? "");
  const [stages, setStages] = useState<DraftStage[]>(pipeline?.stages.map((stage) => ({
    name: stage.name, kind: stage.kind ?? "agent", prompt: stage.prompt ?? "",
    command: stage.command ?? "", model: stage.model ?? "", effort: stage.effort ?? "",
  })) ?? [newAgentStage()]);
  const save = useMutation({
    mutationFn: () => pipeline
      ? api.updatePipeline(pipeline.id, { name, stages: stages.map(stageInput), expected_generation: pipeline.generation })
      : api.createPipeline({ name, stages: stages.map(stageInput) }),
    onSuccess: onSaved,
  });
  const [deleteArmed, setDeleteArmed] = useState(false);
  const remove = useMutation({ mutationFn: () => api.deletePipeline(pipeline!.id), onSuccess: () => onDeleted?.() });
  const update = (index: number, field: keyof DraftStage, value: string) => setStages((current) => current.map((stage, position) => position === index ? { ...stage, [field]: value } : stage));
  const move = (index: number, offset: number) => setStages((current) => {
    const next = [...current];
    const target = index + offset;
    if (target < 0 || target >= next.length) return current;
    [next[index], next[target]] = [next[target], next[index]];
    return next;
  });
  const valid = name.trim() && stages.length > 0 && stages.every((stage, index) =>
    stage.name.trim() && (stage.kind === "agent" ? stage.prompt.trim() :
      stage.kind === "code" ? stage.command.trim() : index > 0 && index === stages.length - 1));
  return <div className="modal-layer" role="presentation">
    <button className="modal-scrim" aria-label="Close Pipeline editor" onClick={onClose} />
    <section className="modal pipeline-modal" role="dialog" aria-modal="true" aria-labelledby="pipeline-editor-title">
      <header className="modal-header"><div><h2 id="pipeline-editor-title">{pipeline ? "Edit Pipeline" : "New Pipeline"}</h2><p>Agent stages start fresh contexts; checks and delivery use no model.</p></div><button className="icon-button" aria-label="Close" onClick={onClose}><X size={17} /></button></header>
      <div className="modal-body pipeline-editor">
        <label className="field"><span>Name</span><input autoFocus value={name} onChange={(event) => setName(event.target.value)} placeholder="Build, test, and review" /></label>
        <div className="pipeline-stage-list">{stages.map((stage, index) => <div className="pipeline-stage-editor" key={index}>
          <div className="pipeline-stage-rail"><span>{index + 1}</span>{index < stages.length - 1 && <i />}</div>
          <div className="pipeline-stage-fields">
            <div className="pipeline-stage-toolbar"><GripVertical size={15} /><strong>{stage.kind === "agent" ? "Agent" : stage.kind === "code" ? "Check" : "Delivery"} stage</strong><span /><button className="icon-button" aria-label={`Move ${stage.name || "stage"} up`} disabled={index === 0} onClick={() => move(index, -1)}>↑</button><button className="icon-button" aria-label={`Move ${stage.name || "stage"} down`} disabled={index === stages.length - 1} onClick={() => move(index, 1)}>↓</button><button className="icon-button" aria-label={`Remove ${stage.name || "stage"}`} disabled={stages.length === 1} onClick={() => setStages((current) => current.filter((_, position) => position !== index))}><Trash2 size={14} /></button></div>
            <label className="field"><span>Stage name</span><input value={stage.name} onChange={(event) => update(index, "name", event.target.value)} placeholder="Review the work" /></label>
            <label className="field"><span>Kind</span><select value={stage.kind} onChange={(event) => update(index, "kind", event.target.value)}><option value="agent">Agent</option><option value="code">Mechanical check</option><option value="delivery">Factory delivery</option></select></label>
            {stage.kind === "agent" && <><label className="field"><span>Prompt template</span><textarea value={stage.prompt} onChange={(event) => update(index, "prompt", event.target.value)} placeholder="Review {{ task.prompt }}" /></label><label className="field"><span>Model</span><input value={stage.model} onChange={(event) => update(index, "model", event.target.value)} placeholder="Inherit" /></label><label className="field"><span>Effort</span><input value={stage.effort} onChange={(event) => update(index, "effort", event.target.value)} placeholder="Inherit" /></label></>}
            {stage.kind === "code" && <label className="field"><span>Command</span><input value={stage.command} onChange={(event) => update(index, "command", event.target.value)} placeholder="go test ./..." /></label>}
            {stage.kind === "delivery" && <p className="muted">Factory pushes the branch, opens the pull request, and reports the outcome without a model.</p>}
          </div>
        </div>)}</div>
        <button className="pipeline-add-stage" disabled={stages.length >= 20} onClick={() => setStages((current) => [...current, newAgentStage("Review", "Review the work for this task:\n{{ task.prompt }}")])}><Plus size={15} /> Add agent stage</button>
        <div className="pipeline-variable-help"><strong>Template variables</strong><code>{"{{ task.prompt }}"}</code><code>{"{{ task.name }}"}</code><code>{"{{ task.id }}"}</code><code>{"{{ repository }}"}</code><code>{"{{ branch }}"}</code><code>{"{{ run.id }}"}</code></div>
        <InlineError error={save.error ?? remove.error} />
      </div>
      <footer className="modal-footer">{pipeline && pipeline.id !== "00000000-0000-0000-0000-000000000001" && pipeline.id !== "00000000-0000-0000-0000-000000000002" && <button className="button button-danger-secondary" disabled={remove.isPending} onClick={() => deleteArmed ? remove.mutate() : setDeleteArmed(true)}>{remove.isPending ? "Deleting…" : deleteArmed ? "Confirm delete" : "Delete"}</button>}<span>{stages.filter((stage) => stage.kind === "agent").length} model start{stages.filter((stage) => stage.kind === "agent").length === 1 ? "" : "s"} per repository</span><button className="button button-secondary" onClick={onClose}>Cancel</button><button className="button button-primary" disabled={save.isPending || remove.isPending || !valid} onClick={() => save.mutate()}>{save.isPending ? "Saving…" : "Save Pipeline"}</button></footer>
    </section>
  </div>;
}
