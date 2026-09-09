# Spawn Tasks from ready beads

Discovers ready work from a [beads](https://github.com/steveyegge/beads) tracker
and spawns one agent Task per bead.

Readiness is decided by the beads CLI, not by Kelos: open beads with no active
blockers, excluding ones that are in progress, blocked, deferred, hooked, or
ephemeral. Because beads models a dependency graph, a bead only becomes visible
to this spawner once whatever it was waiting on is closed — which is stricter
than a label filter can express.

## Files

| File | Purpose |
|------|---------|
| `taskspawner.yaml` | The TaskSpawner with a `when.beads` source |
| `beads-credentials-secret.yaml` | Dolt remote password (and optional user) |
| `credentials-secret.yaml` | Anthropic API key for the agent |
| `workspace.yaml` | Repository the agent works in |
| `github-token-secret.yaml` | Token for pushing branches and opening PRs |

## Prerequisites

None beyond a Kelos install. A beads source needs the beads CLI and `git`, which
the default distroless spawner image cannot run, so the controller automatically
runs beads-sourced spawners on the `kelos-spawner-beads` image. Every other
spawner stays on the smaller default image. Override with
`--spawner-beads-image` (Helm: `spawner.beadsImage`).

## Apply

```bash
kubectl apply -f examples/19-taskspawner-beads/
```

Then check what it found:

```bash
kelos get taskspawners
kelos describe taskspawner beads-ready-work
kubectl get tasks -l kelos.dev/taskspawner=beads-ready-work
```

## Things to get right

**Scope discovery before you enable it.** A shared hub can hold hundreds of
ready beads, and every one becomes a real agent session with real cost. Start
with `labels` / `excludeLabels` narrow, `limit` low, and `maxConcurrency` small,
then widen.

**Nothing writes back to the tracker.** Discovery is read-only: it never claims,
labels, or closes a bead, and never pushes to the hub. A bead stays ready after
its Task finishes, and it is only Task-name deduplication that stops it being
picked up again. If you want the tracker to track reality, instruct the agent to
update the bead itself — as the `promptTemplate` here does.

**A completed Task is not retriggered.** Editing a bead after its Task finished
does not spawn a new one. Delete the Task to rerun it.

**Deduplication rides on the bead ID.** The default Task name is
`<spawner>-<bead ID>`, so the same bead maps to the same Task across cycles.
If you override `nameTemplate`, keep `{{.ID}}` in it or you will spawn
duplicates.

## Template variables

| Variable | Value |
|----------|-------|
| `{{.ID}}` | Bead ID (e.g. `pain-16c090ee`) |
| `{{.Title}}` | Bead title |
| `{{.Body}}` | Bead description |
| `{{.Comments}}` | Bead notes |
| `{{.Labels}}` | Bead labels |
| `{{.Kind}}` | Bead issue type (e.g. `task`, `bug`) |

`{{.Number}}` is `0` and `{{.URL}}` is empty — beads have neither.
