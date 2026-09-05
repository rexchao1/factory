import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { render, screen, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { expect, it, vi } from "vitest";
import { api } from "./api";
import { App } from "./App";
import { RoadmapView, WaitingView } from "./Roadmap";
import type { LoadedRoadmap, RoadmapBoulder, RoadmapCheckpoint } from "./types";

function checkpoint(overrides: Partial<RoadmapCheckpoint> = {}): RoadmapCheckpoint {
  return {
    number: 1, title: "One payer brings itself up live", summary: "the daily ledger allows 20 logins",
    status: "built", planned: true, pebbles: [], passes: [], cost_usd: 0, pass_rounds: 0, ...overrides,
  };
}

function boulder(overrides: Partial<RoadmapBoulder> = {}): RoadmapBoulder {
  return {
    id: "B1", project: "payer", title: "a new payer onboards itself",
    statement: "Give it a new payer portal and it works, unattended.",
    checkpoints: [
      checkpoint(),
      checkpoint({
        number: 2, title: "The dashboard finishes a parked payer", status: "review", cost_usd: 3, pass_rounds: 2,
        pebbles: [{ ordinal: 1, slug: "01-live-driver", title: "Rebuild the live driver" }, { ordinal: 2, slug: "02-publish", title: "Publish to the dashboard" }],
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
    boulders: [boulder()],
    waiting: [{
      boulder: "B1", project: "payer", number: 2, title: "The dashboard finishes a parked payer",
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
    tab="plan" onBoulder={() => {}} onView={() => {}} onWaiting={() => {}} {...props}
  /></QueryClientProvider>);
}

it("lands on one card per boulder rather than every checkpoint at once", async () => {
  vi.spyOn(api, "roadmap").mockResolvedValue(roadmap());
  renderRoadmap();

  const card = await screen.findByRole("button", { name: /a new payer onboards itself/ });
  expect(within(card).getByText("B1")).toBeInTheDocument();
  expect(within(card).getByText("1 of 3 built")).toBeInTheDocument();
  expect(within(card).getByText("$4.50 planned")).toBeInTheDocument();
  expect(within(card).getByText("1 waiting on you")).toBeInTheDocument();
  // The individual checkpoint titles stay inside the boulder, so the landing
  // page cannot grow with them.
  expect(screen.queryByText("The dashboard finishes a parked payer")).not.toBeInTheDocument();
});

it("opens a boulder onto a horizontal checkpoint bar and its first unbuilt rung", async () => {
  vi.spyOn(api, "roadmap").mockResolvedValue(roadmap());
  renderRoadmap({ project: "payer" });

  const bar = await screen.findByRole("navigation", { name: "Checkpoints" });
  expect(within(bar).getAllByRole("button")).toHaveLength(3);
  const panel = screen.getByLabelText("Checkpoint 2");
  expect(within(panel).getByRole("heading", { name: "The dashboard finishes a parked payer" })).toBeInTheDocument();
  expect(within(panel).getByText("Waiting on you")).toBeInTheDocument();
  const pebbles = within(panel).getAllByRole("listitem");
  expect(pebbles.map((item) => item.textContent)).toEqual(["1Rebuild the live driver", "2Publish to the dashboard"]);
  expect(within(panel).getByText("$3.00 across 2 planning passes")).toBeInTheDocument();
});

it("keeps built checkpoints on the roadmap", async () => {
  vi.spyOn(api, "roadmap").mockResolvedValue(roadmap());
  renderRoadmap({ project: "payer", checkpoint: 1 });

  const bar = await screen.findByRole("navigation", { name: "Checkpoints" });
  expect(within(bar).getByText("One payer brings itself up live")).toBeInTheDocument();
  expect(within(screen.getByLabelText("Checkpoint 1")).getByText("Built")).toBeInTheDocument();
});

it("draws the rally for a finished plan and the orbit while an agent is working", async () => {
  vi.spyOn(api, "roadmap").mockResolvedValue(roadmap());
  renderRoadmap({ project: "payer", checkpoint: 2, tab: "planning" });

  expect(await screen.findByRole("img", { name: "2 planning passes" })).toBeInTheDocument();
  expect(screen.getByText("draft")).toBeInTheDocument();
  expect(screen.getByText("$0.75")).toBeInTheDocument();
});

it("shows that an agent is working on a drafting checkpoint", async () => {
  const drafting = boulder({
    checkpoints: [checkpoint({ number: 1, status: "drafting", pass_rounds: 1, passes: [{ at: new Date().toISOString(), mode: "draft", round: 1, cost_usd: 1 }] })],
    built_count: 0,
  });
  vi.spyOn(api, "roadmap").mockResolvedValue(roadmap({ boulders: [drafting], waiting: [] }));
  renderRoadmap({ project: "payer", checkpoint: 1, tab: "planning" });

  expect(await screen.findByRole("img", { name: "Planning agents are running" })).toBeInTheDocument();
  expect(screen.getAllByText("Agent working").length).toBeGreaterThan(0);
});

it("says what to configure when no roadmap root is set", async () => {
  vi.spyOn(api, "roadmap").mockResolvedValue({ configured: false, boulders: [], waiting: [], read_at: "" });
  renderRoadmap();

  expect(await screen.findByText("No roadmap is configured")).toBeInTheDocument();
});

it("lists what is waiting with the action to take", async () => {
  vi.spyOn(api, "roadmap").mockResolvedValue(roadmap());
  const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  const opened = vi.fn();
  render(<QueryClientProvider client={client}><WaitingView onBoulder={opened} /></QueryClientProvider>);

  const card = await screen.findByRole("button", { name: /The dashboard finishes a parked payer/ });
  expect(within(card).getByText("B1.2")).toBeInTheDocument();
  expect(within(card).getByText("The plan is written and waiting for your answers.")).toBeInTheDocument();
  await userEvent.click(card);
  expect(opened).toHaveBeenCalledWith("payer", 2);
});

it("badges the sidebar with what is waiting and routes into a boulder", async () => {
  vi.spyOn(api, "roadmap").mockResolvedValue(roadmap());
  vi.spyOn(api, "factoryPause").mockResolvedValue({ paused: false });
  vi.spyOn(api, "workers").mockResolvedValue([]);
  const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  render(<QueryClientProvider client={client}><App /></QueryClientProvider>);

  expect(await screen.findByLabelText("1 waiting")).toHaveTextContent("1");
  await userEvent.click(screen.getByRole("button", { name: "Roadmap" }));
  await userEvent.click(await screen.findByRole("button", { name: /a new payer onboards itself/ }));
  expect(window.location.pathname).toBe("/roadmap/payer");
  await userEvent.click(await screen.findByRole("tab", { name: "Planning" }));
  expect(window.location.search).toBe("?c=2&tab=planning");
});
