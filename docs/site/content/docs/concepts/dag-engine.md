---
title: "DAG engine"
description: "How kevin orders, runs, and removes a graph of steps."
weight: 4
---

# DAG engine

The DAG engine holds a map from each step name to the names it needs. It checks the map before anything runs: an unknown name or a cycle is an error.

## Starting

The engine runs each step in its own goroutine. A goroutine waits for the steps it needs, then runs its step. `engine.max_parallel` limits how many steps run at once.

When a step fails, the engine cancels the steps still running. A step whose dependency failed is skipped, not failed, so the error kevin reports is the cause, not a list of failures that follow from it.

The engine returns the outputs of every step that finished. kevin uses this list to remove the steps that came up. It also removes a step whose `Up` was still running when the run was canceled.

## Removing

Teardown reverses every edge and uses the same scheduler. Steps are removed in parallel wherever the graph allows.

## Rerunning a step

A rerun walks the same graph again, from the step you name. The steps it does not touch keep the outputs they recorded, so the rerun step sees its dependencies' real outputs without starting them again. The console, the `rerun_step` MCP tool, and [`kevin rerun`]({{< relref "/docs/reference/commands/rerun" >}}) all start one.

The named step always runs. With `cascade`, its transitive dependents join it. A dependent that never completed, because a failure upstream skipped it, always joins. A dependent that already completed joins only if its step type is idempotent, so a cascade does not repeat side effects that cannot safely run twice.

A step that is already running rejects the rerun with an error instead of queuing it. A name the environment does not declare is also an error.

## Step groups

A [step group]({{< relref "/docs/reference/environment-file#step-groups" >}}) adds one node to the map. The group's node needs every member. Its work is to compute the group's `outputs` from its members' outputs, with no plugin call. A member's name is not visible outside its group.
