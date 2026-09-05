import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { render, screen, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { expect, it, vi } from "vitest";
import { api } from "./api";
import { App } from "./App";
import { RoadmapView, WaitingView } from "./Roadmap";
import type { LoadedRoadmap, RoadmapCheckpoint, RoadmapProject } from "./types";

function checkpoint(overrides: Partial<RoadmapCheckpoint> = {}): RoadmapCheckpoint {
  return {
    number: 1, title: "One payer brings itself up live", summary: "the daily ledger allows 20 logins",
    status: "built", planned: true, boulders: [], pebbles: [], passes: [], cost_usd: 0, pass_rounds: 0, ...overrides,
  };
}

const driver = { ordinal: 1, slug: "01-live-driver", title: "Rebuild the live driver", summary: "The runner loses the driver on a resume.", state: "running" as const, work_id: "w-1" };
const publish = { ordinal: 2, slug: "02-publish", title: "Publish to the dashboard", state: "succeeded" as const, pull_request_url: "https://example.test/pr/2" };

function project(overrides: Partial<RoadmapProject> = {}): RoadmapProject {
  return {
    project: "payer", title: "a new payer onboards itself",
    statement: "Give it a new payer portal and it works, unattended.",
    checkpoints: [
      checkpoint(),
      checkpoint({
        number: 2, title: "The dashboard finishes a parked payer", status: "review", cost_usd: 3, pass_rounds: 2,
        boulders: [
          { id: "B1", title: "Bring the driver back", statement: "The driver survives a resume.", pebbles: [driver], state: "working" },
          { id: "B2", title: "Show it on the dashboard", pebbles: [publish], state: "done" },
        ],
        pebbles: [driver, publish],
        passes: [
          { at: "2026-09-04T07:43:04Z", mode: "draft", round: 1, cost_usd: 2.25, outcome: "ok" },
          { at: "2026-09-04T07:48:46Z", mode: "critique", round: 1, cost_usd: 0.75, outcome: "ok" },
        ],
      }),
      checkpoint({ number: 3, title: "A payer's profile is data", status: "planned", planned: false, summary: "" }),
    ],
    cost_usd: 4.5, built_count: 1, ...overrides,
  };
}

function roadmap(overrides: Partial<LoadedRoadmap> = {}): LoadedRoadmap {
  return {
    configured: true,
    projects: [project()],
    waiting: [{
      project: "payer", number: 2, title: "The dashboard finishes a parked payer",
      status: "review", reason: "The plan is written and waiting for your answers.",
      action: "Review the plan", cost_usd: 3, pass_rounds: 2,
    }],
    read_at: "2026-09-05T09:00:00Z",
    ...overrides,
  };
}

function renderRoadmap(props: Partial<Parameters<typeof RoadmapView>[0]> = {}) {
  const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  render(<QueryClientProvider client={client}><RoadmapView
    onProject={() => {}} onView={() => {}} onWaiting={() => {}} onWork={() => {}} {...props}
  /></QueryClientProvider>);
}

it("lands on one card per project rather than every checkpoint at once", async () => {
  vi.spyOn(api, "roadmap").mockResolvedValue(roadmap());
  renderRoadmap();

  const card = await screen.findByRole("button", { name: /a new payer onboards itself/ });
  expect(within(card).getByText("1 of 3 built")).toBeInTheDocument();
  expect(within(card).getByText("$4.50 planned")).toBeInTheDocument();
  expect(within(card).getByText("1 waiting on you")).toBeInTheDocument();
  // The individual checkpoint titles stay inside the project, so the landing
  // page cannot grow with them.
  expect(screen.queryByText("The dashboard finishes a parked payer")).not.toBeInTheDocument();
});

it("opens a project onto a horizontal checkpoint bar and its first unbuilt rung", async () => {
  vi.spyOn(api, "roadmap").mockResolvedValue(roadmap());
  renderRoadmap({ project: "payer" });

  const bar = await screen.findByRole("navigation", { name: "Checkpoints" });
  expect(within(bar).getAllByRole("button")).toHaveLength(3);
  const detail = screen.getByLabelText("Details");
  expect(within(detail).getByRole("heading", { name: "The dashboard finishes a parked payer" })).toBeInTheDocument();
  expect(within(detail).getByText("Waiting on you")).toBeInTheDocument();
  expect(within(detail).getByText("$3.00")).toBeInTheDocument();
});

it("shows the boulders big, coloured by what the factory is doing with them", async () => {
  vi.spyOn(api, "roadmap").mockResolvedValue(roadmap());
  renderRoadmap({ project: "payer", checkpoint: 2 });

  const working = await screen.findByRole("button", { name: /Bring the driver back/ });
  expect(working.closest("section")).toHaveClass("boulder-working");
  expect(within(working).getByText("Being built")).toBeInTheDocument();
  expect(within(working).getByText("0/1 done")).toBeInTheDocument();
  const done = screen.getByRole("button", { name: /Show it on the dashboard/ });
  expect(done.closest("section")).toHaveClass("boulder-done");
  // Pebbles stay hidden until their boulder is opened.
  expect(screen.queryByText("Rebuild the live driver")).not.toBeInTheDocument();
});

it("drops the pebbles under a boulder and reads one on the right", async () => {
  vi.spyOn(api, "roadmap").mockResolvedValue(roadmap());
  const opened = vi.fn();
  renderRoadmap({ project: "payer", checkpoint: 2, onWork: opened });

  await userEvent.click(await screen.findByRole("button", { name: /Bring the driver back/ }));
  const pebble = screen.getByRole("button", { name: /Rebuild the live driver/ });
  await userEvent.click(pebble);

  const detail = screen.getByLabelText("Details");
  expect(within(detail).getByRole("heading", { name: "Rebuild the live driver" })).toBeInTheDocument();
  expect(within(detail).getByText("The runner loses the driver on a resume.")).toBeInTheDocument();
  expect(within(detail).getByText("01-live-driver.md")).toBeInTheDocument();
  await userEvent.click(within(detail).getByRole("button", { name: "Open the run" }));
  expect(opened).toHaveBeenCalledWith("w-1");
});

it("says a checkpoint has nothing to show rather than an empty stage", async () => {
  vi.spyOn(api, "roadmap").mockResolvedValue(roadmap());
  renderRoadmap({ project: "payer", checkpoint: 3 });

  expect(await screen.findByText("Still a line on the route. Nothing has been drafted for it yet.")).toBeInTheDocument();
});

it("keeps built checkpoints on the roadmap", async () => {
  vi.spyOn(api, "roadmap").mockResolvedValue(roadmap());
  renderRoadmap({ project: "payer", checkpoint: 1 });

  const bar = await screen.findByRole("navigation", { name: "Checkpoints" });
  expect(within(bar).getByText("One payer brings itself up live")).toBeInTheDocument();
  expect(within(screen.getByLabelText("Details")).getByText("Built")).toBeInTheDocument();
});

it("says what to configure when no roadmap root is set", async () => {
  vi.spyOn(api, "roadmap").mockResolvedValue({ configured: false, projects: [], waiting: [], read_at: "" });
  renderRoadmap();

  expect(await screen.findByText("No roadmap is configured")).toBeInTheDocument();
});

it("lists what is waiting with the action to take", async () => {
  vi.spyOn(api, "roadmap").mockResolvedValue(roadmap());
  const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  const opened = vi.fn();
  render(<QueryClientProvider client={client}><WaitingView onProject={opened} /></QueryClientProvider>);

  const card = await screen.findByRole("button", { name: /The dashboard finishes a parked payer/ });
  expect(within(card).getByText("The plan is written and waiting for your answers.")).toBeInTheDocument();
  await userEvent.click(card);
  expect(opened).toHaveBeenCalledWith("payer", 2);
});

it("rings one red bell in the top bar and routes into a project", async () => {
  vi.spyOn(api, "roadmap").mockResolvedValue(roadmap());
  vi.spyOn(api, "factoryPause").mockResolvedValue({ paused: false });
  vi.spyOn(api, "workers").mockResolvedValue([]);
  const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  render(<QueryClientProvider client={client}><App /></QueryClientProvider>);

  const bell = await screen.findByLabelText("1 thing waiting for you");
  expect(bell).toHaveTextContent("1");
  expect(bell).toHaveClass("ringing");
  await userEvent.click(screen.getByRole("button", { name: "Roadmap" }));
  await userEvent.click(await screen.findByRole("button", { name: /a new payer onboards itself/ }));
  expect(window.location.pathname).toBe("/roadmap/payer");
  await userEvent.click(bell);
  expect(window.location.pathname).toBe("/waiting");
});

it("has no Pipelines page left to navigate to", async () => {
  vi.spyOn(api, "roadmap").mockResolvedValue(roadmap({ waiting: [] }));
  vi.spyOn(api, "factoryPause").mockResolvedValue({ paused: false });
  vi.spyOn(api, "workers").mockResolvedValue([]);
  const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  render(<QueryClientProvider client={client}><App /></QueryClientProvider>);

  expect(await screen.findByRole("button", { name: "Drafts" })).toBeInTheDocument();
  expect(screen.queryByRole("button", { name: "Pipelines" })).not.toBeInTheDocument();
  expect(screen.getByRole("button", { name: "Planning" })).toBeInTheDocument();
});
