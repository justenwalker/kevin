---
title: "DAG engine"
description: "How kevin schedules, validates, and tears down a graph of steps."
weight: 4
---

# DAG engine

The DAG engine holds a map of step names to dependency names, and validates it up front: an unknown dependency or a cycle fails before anything runs.

Bringing an environment up runs one goroutine per step. A goroutine waits for each of its dependencies to finish, then runs the step. The first step that fails cancels every other step still in flight.

A step whose dependency already failed is skipped, not failed in its own right, so the error a run reports is the root cause, not a cascade of secondary failures downstream of it.

Bringing an environment up returns the outputs of every step that completed; the engine uses that to remove exactly the steps that came up.

Tearing an environment down reverses every dependency edge and removes steps with the same scheduler, so removal is parallel wherever the graph allows it, the same as bringing it up.
