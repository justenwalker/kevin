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

## Step groups

A [step group]({{< relref "/docs/reference/environment-file#step-groups" >}}) adds one node to the map. The group's node needs every member. Its work is to compute the group's `outputs` from its members' outputs, with no plugin call. A member's name is not visible outside its group.
