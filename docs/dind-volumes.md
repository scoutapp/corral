# Docker-in-Docker: volumes & caches

Turn on **Docker-in-Docker (DinD)** in a project's **Config** tab and the sandbox
gets its own inner Docker daemon. You can `docker build`, `docker run`, spin up
Postgres — all inside the box.

## Where inner-Docker data lives

The inner daemon's data root (images + named volumes) is a **named Docker volume**
on your Mac, keyed to the project's workspace path:

```
corral-dind-<hash-of-workspace>
```

That means:

- **Images and volumes survive restarts.** `docker pull postgres:16` once; it's
  cached next time. A Postgres data volume comes back with its data.
- **Each project is isolated.** Two projects never share inner-Docker state.
- Inner containers themselves are destroyed on exit — restart them; their volumes
  are intact.

```bash
# Inside the sandbox, after a restart — the volume still has your data
docker run -d -p 5432:5432 -v pgdata:/var/lib/postgresql/data postgres:16
```

## Reaching an inner service

Publish the port with `-p` so it's reachable (and viewable in [Live View](live-view.md)):

```bash
docker run -d -p 3000:3000 myapp        # reachable
docker run -d --expose 3000 myapp       # NOT reachable — no -p
```

## Data caches — start from a prebuilt state

Building an image and seeding a database every time a fresh project starts is slow.
A **cache** is a reusable snapshot of a project's inner-Docker data that a new
project can start *from*. Manage it in **Config**, under the DinD section (shown
when DinD is on).

### Save one

Get a project's inner Docker into the state you want (images built, DB seeded),
then **Save current data as cache** and name it, e.g. `pg16-seeded`. It copies the
volume — big data roots take a moment.

### Start a project from it

In a project's DinD config, pick the cache and a **mode**:

| Mode | What happens | Use it for |
|------|--------------|-----------|
| **Copy** | The project gets a fresh copy of the cache; changes never touch the cache. | Throwaway work — the default. |
| **Shared** | The project mounts the cache directly; changes (e.g. a migration) persist back into it. | Updating the cache itself. |

Changing the cache is restart-required.

## Gotchas

- **Copy mode seeds once**, on the project's *first* start. After that the project
  has its own volume and diverges — re-pointing won't re-copy.
- **Shared mode is shared.** Two projects on the same cache in shared mode will
  step on each other. Use copy unless you specifically want writes to persist.
- Wipe a project's inner Docker entirely with `corral rebuild --destroy-inner`.
