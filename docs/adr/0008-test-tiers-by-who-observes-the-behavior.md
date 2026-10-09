# ADR-0008: Test tiers by who observes the behavior

**Status:** Accepted

## Context

kevin has three kinds of test: unit tests (`go test ./...`), integration
tests (the `integration` build tag, GO-017), and end-to-end tests
(`tests/e2e`, the `e2e` build tag, driving the built binary). The split was
never written down, so tests landed in whichever tier was convenient. The
two slow tiers now take about the same time, because both create the same
real kind, k3d and minikube clusters, and several checks (a mount reaching
every node, the reserved node fields, reuse of an existing cluster) are made
in more than one tier.

## Decision

A test belongs to the tier of whoever would notice the behavior breaking.

- **Unit:** one package in isolation, with fakes. No daemon, no network
  beyond loopback.
- **Integration:** one component against a real dependency (Docker, kind,
  k3d, minikube, a registry), driven in-process through Go APIs. It asserts
  the seam with that dependency and what happens when it misbehaves:
  idempotent `Up` and `Down`, reuse and rebuild, recovery after a crash,
  retain on failure, concurrency, error paths, and what the component left
  behind (labels, networks, node files).
- **E2E:** the built `kevin` binary, one flow per user task, named in the
  suite's doc comment. It asserts only what a user can see: CLI output, exit
  codes, the console, proxy responses, files in the project.

The decision rule, in order:

1. Can it be checked without a daemon? It is a unit test.
2. Does it need an in-process handle, inspect docker or node internals, or
   inject a failure? It is an integration test.
3. Can a user observe it without knowing how kevin works? It is an e2e test.

An e2e test never asserts what a lower tier already proves. If an e2e flow
passes, the internals it depends on are not re-checked there.

Every integration and e2e suite's doc comment ends with its tier, and an e2e
suite's first sentence names the user task it covers ([GO-020](../GO_CONVENTIONS.md#go-020-an-integration-or-e2e-suites-doc-comment-ends-with-its-tier)).
Unit tests carry no marker: a test not gated by the `integration` or `e2e`
build tag is a unit test.

## Why

Cost follows the tier. A cluster takes about a minute to create, so every
assertion that could run against a cluster another test already created, but
instead creates its own, adds a minute. Putting internals checks in
integration, where one suite shares one cluster, and keeping e2e to one pass
per user flow, pays for each cluster once per question it answers.

The tiers also fail differently. An e2e failure should read as "a user
would see this break." An integration failure should point at one component
and one dependency. A check that sits in the wrong tier gives the wrong
signal: a docker label assertion failing in e2e says nothing about the user
flow, and a user flow only exercised in integration can pass while the CLI is
broken.

**DO** (`internal/plugins/kubernetes/kind_integration_test.go`, a seam check
against a real cluster):
```go
// TestKindNodeSettings proves node-level settings reach a real cluster: a
// mount lands in every node, control_plane extraMounts land on the control
// plane alone, and a worker's own image applies to that node only.
func TestKindNodeSettings(t *testing.T) { ... }
```

**DO** (`tests/e2e/kind_test.go`, a user flow through the binary):
```go
// TestAppRouteReachesTheServiceThroughTheRelay covers the relay-routed
// HTTPS route: app.kevin.home reaches the nginx Service through the
// cluster's own SOCKS5 relay.
func (s *KindSuite) TestAppRouteReachesTheServiceThroughTheRelay() { ... }
```

**DO NOT** (an e2e test of this shape, internals a user never sees):
```go
// Checks a mount through docker exec into a node, which the integration
// test above already proves against the same driver.
func (s *KindSuite) TestMountsReachTheNodes() {
	s.Contains(s.dockerOut("exec", controlPlane, "ls", "/host-all"), "all.txt")
	...
}
```
`docker exec` into a node is not something a user does. The test costs a
cluster bring-up, and its failure would not tell a user anything they could
act on.

## Consequences

Moving a test to the right tier is a normal change, with no compatibility
step (ADR-0001). An e2e check that has no user-visible effect is deleted,
not kept for safety. A behavior that needs both an integration check of its
mechanism and an e2e check of its flow gets both, and each says in its
comment which half it covers.

The cost is judgment at the edges. A crash followed by a successful rerun is
both a recovery case and a user flow. The rule above resolves it by asking
what the test asserts: the user-visible rerun is e2e, and what the crash
left behind is integration. A test that asserts both is split in two.
