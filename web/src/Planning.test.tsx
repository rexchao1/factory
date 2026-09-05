import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { render, screen, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { expect, it, vi } from "vitest";
import { api } from "./api";
import { PlanningView } from "./Planning";
import type { LoadedRoadmap, RoadmapCheckpoint } from "./types";

function checkpoint(overrides: Partial<RoadmapCheckpoint> = {}): RoadmapCheckpoint {
  return {
    number: 1, title: "One payer brings itself up live", summary: "the daily ledger allows 20 logins",
    status: "planned", planned: false, boulders: [], pebbles: [], passes: [], cost_usd: 0, pass_rounds: 0, ...overrides,
  };
}

function roadmap(checkpoints: RoadmapCheckpoint[]): LoadedRoadmap {
  return {
    configured: true,
    projects: [{ project: "payer", title: "a new payer onboards itself", checkpoints, cost_usd: 4.5, built_count: 0 }],
    waiting: [],
    read_at: "2026-09-05T09:00:00Z",
  };
}

function renderPlanning(data: LoadedRoadmap, onProject = vi.fn()) {
  vi.spyOn(api, "roadmap").mockResolvedValue(data);
  const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  render(<QueryClientProvider client={client}><PlanningView onProject={onProject} /></QueryClientProvider>);
  return onProject;
}

const drafting = checkpoint({
  number: 2, title: "The dashboard finishes a parked payer", status: "drafting", planned: true,
  cost_usd: 3, pass_rounds: 2,
  passes: [
    { at: "2026-09-04T07:43:04Z", mode: "draft", round: 1, cost_usd: 2.25, outcome: "ok" },
    { at: "2026-09-04T07:48:46Z", mode: "critique", round: 1, cost_usd: 0.75, outcome: "ok" },
  ],
});

it("shows the checkpoint being written, its rail, and its passes", async () => {
  renderPlanning(roadmap([drafting]));

  expect(await screen.findByRole("heading", { name: "The dashboard finishes a parked payer" })).toBeInTheDocument();
  expect(screen.getByRole("img", { name: "Planning agents are running" })).toBeInTheDocument();
  expect(screen.getByRole("img", { name: "2 planning passes" })).toBeInTheDocument();
  expect(screen.getByText("$3.00 · 2 passes")).toBeInTheDocument();
  // The last pass was a critique, so that is the stop it is standing on.
  const rail = screen.getByRole("list", { name: "Planning stages" });
  expect(within(rail).getByText("Critique").closest("li")).toHaveClass("here");
});

it("leaves a checkpoint that is only a line on the route off the page", async () => {
  renderPlanning(roadmap([checkpoint()]));

  expect(await screen.findByText("Nothing is being planned")).toBeInTheDocument();
});

it("leaves a frozen checkpoint that already has pebbles off the page", async () => {
  renderPlanning(roadmap([checkpoint({
    number: 2, status: "frozen", planned: true,
    pebbles: [{ ordinal: 1, slug: "01-a", title: "A pebble" }],
  })]));

  expect(await screen.findByText("Nothing is being planned")).toBeInTheDocument();
});

it("puts what is actually turning first and switches between them", async () => {
  const stuck = checkpoint({ number: 3, title: "A payer's profile is data", status: "fog", planned: true });
  renderPlanning(roadmap([stuck, drafting]));

  const chips = await screen.findAllByRole("button", { name: /checkpoint|payer|\d\./ });
  const strip = screen.getByText("2. The dashboard finishes a parked payer").closest("button");
  expect(strip).toHaveClass("active");
  expect(chips.length).toBeGreaterThan(1);

  await userEvent.click(screen.getByText("3. A payer's profile is data"));
  expect(screen.getByRole("heading", { name: "A payer's profile is data" })).toBeInTheDocument();
  expect(screen.getByText("Stuck on questions")).toBeInTheDocument();
});

it("hands a checkpoint back to the roadmap", async () => {
  const onProject = renderPlanning(roadmap([drafting]));

  await userEvent.click(await screen.findByRole("button", { name: "Open on the roadmap" }));
  expect(onProject).toHaveBeenCalledWith("payer", 2);
});
