# PolyCI

PolyCI is a local debugger for CI pipelines. Instead of editing a
script, pushing, and waiting for a remote runner to fail again, you
run the failing job on your own machine, pause it step-by-step, and
drop into a real shell inside the exact container where it broke — no
separate debug action, no guesswork about what the runner's
environment looked like.

It works across GitLab CI, CircleCI, and GitHub Actions: it parses
your existing `.gitlab-ci.yml`, CircleCI `config.yml`, or GitHub
Actions workflow file, converts it into one internal provider-agnostic
pipeline model (jobs → steps), and runs it against your local Docker
engine, streaming logs to the terminal exactly the way real CI would.

## Why this project exists

No local CI tool — including `act` — ships a built-in step-by-step
debugger or an interactive on-failure shell. That's the actual gap
PolyCI fills. Building it properly meant not tying the debugger to one
provider's format, which turned out to close a second gap for free:
GitLab CI and CircleCI have no local runner at all today, `act`-style
or otherwise, so PolyCI covers those too.

## Relationship to `act`

[`act`](https://github.com/nektos/act) is a mature, actively developed
local runner for GitHub Actions, with a large community and editor
integrations. **PolyCI is not trying to replace or out-compete `act`
on GitHub Actions** — it already does that job well, and duplicating
it isn't where this project adds value.

PolyCI's actual differentiator is the debugger:

1. **A built-in step-by-step debugger and shell-on-fail** (pause after
   every step, choose continue/abort, or drop into a real shell inside
   the failing container) — a genuine gap even for GitHub Actions
   users of `act`, and the reason this project exists.
2. It happens to also cover **GitLab CI and CircleCI**, where no
   equivalent local runner exists at all otherwise — a side effect of
   building the debugger on a provider-agnostic engine rather than a
   consequence of targeting those providers specifically.
3. **One tool for repos that use more than one CI provider**, instead
   of juggling a separate local runner per provider.

If you only use GitHub Actions and don't need step-by-step debugging
or shell-on-fail, `act` remains the more mature, better-supported
choice on its own.

## Installation

PolyCI is a single Go binary with no runtime dependencies beyond a
running Docker engine.

**Requirements:**
- A running Docker engine reachable from `DOCKER_HOST` or the active
  Docker CLI context — Docker Desktop, [Colima](https://github.com/abiosoft/colima),
  Rancher Desktop, or any other engine that speaks the standard Docker
  API all work
- [Go](https://go.dev/) 1.27 or later, only if installing via `go install`
  or building from source

### Install via Homebrew

The recommended way to install on macOS or Linux:

```sh
brew tap sqweyyy-create/polyci
brew install polyci
```

(On newer Homebrew versions, tapping a third-party repository for the
first time may ask you to run `brew trust sqweyyy-create/polyci`
before it will install from it.)

### Alternative: `go install`

```sh
go install github.com/sqweyyy-create/PolyCI/cmd/polyci@latest
```

This puts `polyci` in `$(go env GOPATH)/bin` — make sure that's on
your `$PATH`.

### Alternative: build from source

```sh
git clone https://github.com/sqweyyy-create/PolyCI.git
cd PolyCI
go build -o polyci ./cmd/polyci
```

This produces a `polyci` binary in the current directory. Move it
onto your `$PATH` (e.g. `mv polyci /usr/local/bin/`) if you want to
run it from anywhere.

## Usage

Run `polyci run` from the root of the project whose pipeline you want
to debug or test — the current directory is bind-mounted read-write
into every job's container at `/workspace`, so jobs see your actual
files, the same way a real CI checkout would.

### Debugging

Add `-debug` to pause after every step, see its result, and choose
whether to continue or abort — or drop into a manual shell in the
job's container by answering `s`:

```sh
polyci run -debug
polyci run -provider circleci -debug
polyci run -provider github-actions -f .github/workflows/ci.yml -debug
```

```
[build] step "script[0]" done — continue? [Y/n/a=abort/s=shell]
```

Add `-shell-on-fail` to automatically drop into an interactive shell
in a job's container the moment a step fails, without needing
`-debug`'s per-step prompts:

```sh
polyci run -shell-on-fail
```

Both flags can be combined: shell on failure, then still get asked to
continue or abort once you exit the shell.

When a step failed and you enter its shell (either way), exiting the
shell offers a third option alongside continue/abort:

```
[build] step "script[0]": [c]ontinue/[a]bort/[r]etry?
```

`r` re-runs that exact step — same container, same command — instead
of moving on or stopping the pipeline. If it fails again, you get the
same prompt again; if it succeeds, the pipeline continues normally
from the next step.

### Planning (dry run)

`polyci plan` parses a config and prints its resolved structure —
jobs, their dependencies, and which would run in parallel versus
sequentially — without running anything. No Docker container is
created and no Docker engine connection is even attempted, so it
works even without Docker running:

```sh
polyci plan
polyci plan -provider circleci
polyci plan -provider github-actions -f .github/workflows/ci.yml
```

```
Plan for .gitlab-ci.yml (3 job(s)):

Level 0: build
  - build [stage=build image=alpine:3.19] depends on: (none)
Level 1 (parallel): test, lint
  - test [stage=test image=alpine:3.19] depends on: build
  - lint [stage=test image=alpine:3.19] depends on: build

Levels run one after another; jobs within the same level run in parallel.
```

### Running a pipeline

The same `polyci run` works across all three providers:

**GitLab CI:**

```sh
# Looks for .gitlab-ci.yml in the current directory by default
polyci run

# Or point at a specific file
polyci run -provider gitlab -f path/to/.gitlab-ci.yml
```

**CircleCI:**

```sh
# Looks for .circleci/config.yml in the current directory by default
polyci run -provider circleci

# Or point at a specific file
polyci run -provider circleci -f path/to/config.yml
```

**GitHub Actions:**

Workflow files can be named anything under `.github/workflows/`, so
there's no sensible default — `-f` is required:

```sh
polyci run -provider github-actions -f .github/workflows/ci.yml
```

Only jobs with an explicit `container:` are runnable (see
[Known Limitations](#known-limitations)).

### Service containers

Jobs that need a database or other backing service can declare one —
GitLab's `services:` and CircleCI's additional `docker:` entries are
both supported. Each service starts in its own container on a network
shared with the job, so the job can reach it by hostname; both the
service containers and the network are cleaned up when the job
finishes, whether it succeeds or fails.

**GitLab CI:**

```yaml
test:
  image: postgres:16-alpine
  services:
    - name: postgres:16-alpine
      alias: postgres
      variables:
        POSTGRES_PASSWORD: testpass
  script:
    - psql -h postgres -U postgres -c "SELECT 1"
```

A bare string (`- postgres:16-alpine`) works too — the hostname then
defaults to the image name without its registry path or tag, so
`postgres:16-alpine` becomes reachable as `postgres`. `services:` can
also be set once at the top level as the default for every job; a
job's own `services:` replaces that default rather than adding to it.

**CircleCI:**

```yaml
jobs:
  build:
    docker:
      - image: cimg/base:2023.03
      - image: postgres:16-alpine
        name: postgres
        environment:
          POSTGRES_PASSWORD: testpass
    steps:
      - run: psql -h postgres -U postgres -c "SELECT 1"
```

The first `docker:` entry is the job's own image; every entry after
it becomes a service, reachable by its `name:` (or, if that's
omitted, the same default-from-image-name rule as GitLab).

## Known Limitations

- The workspace bind mount requires the Docker engine to be able to
  see the host path being mounted. Colima (a common local Docker
  engine for macOS) only shares `$HOME` into its VM by default — a
  project outside your home directory will fail to mount with "bind
  source path does not exist". If you hit this, either move the
  project under `$HOME` or add its path to Colima's `mounts:` config
  (`~/.colima/default/colima.yaml`) and restart Colima.
- Containers run as root by default, so files a job writes into
  `/workspace` typically show up on the host owned by root (or
  whatever UID the container image defaults to) rather than your own
  user — a known annoyance shared with other local-CI-runner tools,
  not yet addressed here (e.g. by matching the container's user to
  the host UID).
- GitHub Actions' equivalent of service containers (a job's
  `services:`) isn't supported yet — only GitLab CI and CircleCI have
  it today (see [Service containers](#service-containers) above).
- The CircleCI parser only supports the `docker` executor (not
  `machine`, `macos`, or `windows`), doesn't expand `orbs:` or
  top-level `commands:`, and doesn't support aliasing a job to a
  different name via `name:` in a workflow's job list. Unsupported
  builtin steps (`save_cache`, `restore_cache`, `persist_to_workspace`,
  `attach_workspace`, `store_artifacts`, `store_test_results`,
  `setup_remote_docker`, `add_ssh_keys`, `deploy`) become a visible
  no-op log line rather than erroring, so real-world configs still
  parse and run.
- The GitHub Actions parser only runs jobs that declare an explicit
  `container:` — GitHub's default runner model executes steps
  directly on a VM (`runs-on: ubuntu-latest`) rather than in a
  container, and approximating that with a Docker image (as `act`
  does, with its own curated image set) is out of scope; a job
  without `container:` fails to parse with a clear error rather than
  silently doing nothing. It doesn't evaluate `${{ }}` expressions
  (github/env/matrix context, etc.) — any that appear in `run:` are
  passed through to the shell literally, which will usually error —
  and doesn't expand `strategy: matrix:` (each job runs once, exactly
  as written) or `uses:` a local/composite action. Only one workflow
  file is run per invocation, not every file in that directory.
- Phase 3's shell-on-fail feature is verified manually with a real
  TTY, not by an automated test — a `github.com/creack/pty`-based Go
  test was attempted but hung unreliably and was removed rather than
  left flaky in the suite.

See [`CLAUDE.md`](./CLAUDE.md) for the full build rationale, phase
history, and development conventions.

## License

[MIT](./LICENSE)
