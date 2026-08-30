# PolyCI

PolyCI is a CLI tool that runs CI/CD pipelines locally in Docker
containers, with a built-in step-by-step debugger, across multiple CI
providers — starting where no good local tool currently exists.

It parses your existing `.gitlab-ci.yml`, CircleCI `config.yml`, or
GitHub Actions workflow file, converts it into one internal
provider-agnostic pipeline model (jobs → steps), and runs it against
your local Docker engine, streaming logs to the terminal exactly the
way real CI would.

## Why this project exists

GitLab CI and CircleCI have no equivalent of GitHub's `act` — there is
currently no good way to run their pipelines locally before pushing.
PolyCI fills that gap, and adds something no local runner (including
`act`) currently has: a step-by-step debugger and an interactive
on-failure shell, built in rather than bolted on as a separate
action.

## Relationship to `act`

[`act`](https://github.com/nektos/act) is a mature, actively developed
local runner for GitHub Actions, with a large community and editor
integrations. **PolyCI is not trying to replace or out-compete `act`
on GitHub Actions** — it already does that job well, and duplicating
it isn't where this project adds value.

PolyCI's actual differentiators are:

1. **GitLab CI and CircleCI support**, where no equivalent local
   runner exists at all today — that's where this project starts and
   where most of its value is.
2. **A built-in step-by-step debugger and shell-on-fail** (pause after
   every step, choose continue/abort, or drop into a real shell inside
   the failing container) — a genuine gap even for GitHub Actions
   users of `act`, and available for all three providers here.
3. **One tool for repos that use more than one CI provider**, instead
   of juggling a separate local runner per provider.

The GitHub Actions parser exists mainly to serve that third point —
if you only use GitHub Actions, `act` remains the better-supported
choice.

## Installation

PolyCI is a single Go binary with no runtime dependencies beyond a
running Docker engine.

**Requirements:**
- [Go](https://go.dev/) 1.27 or later
- A running Docker engine reachable from `DOCKER_HOST` or the active
  Docker CLI context — Docker Desktop, [Colima](https://github.com/abiosoft/colima),
  Rancher Desktop, or any other engine that speaks the standard Docker
  API all work

**Build from source:**

```sh
git clone <this repo's URL>
cd polyci
go build -o polyci ./cmd/polyci
```

This produces a `polyci` binary in the current directory. Move it
onto your `$PATH` (e.g. `mv polyci /usr/local/bin/`) if you want to
run it from anywhere.

## Usage

Run `polyci run` from the root of the project whose pipeline you want
to test — the current directory is bind-mounted read-write into every
job's container at `/workspace`, so jobs see your actual files, the
same way a real CI checkout would.

### GitLab CI

```sh
# Looks for .gitlab-ci.yml in the current directory by default
polyci run

# Or point at a specific file
polyci run -provider gitlab -f path/to/.gitlab-ci.yml
```

### CircleCI

```sh
# Looks for .circleci/config.yml in the current directory by default
polyci run -provider circleci

# Or point at a specific file
polyci run -provider circleci -f path/to/config.yml
```

### GitHub Actions

Workflow files can be named anything under `.github/workflows/`, so
there's no sensible default — `-f` is required:

```sh
polyci run -provider github-actions -f .github/workflows/ci.yml
```

Only jobs with an explicit `container:` are runnable (see
[Known Limitations](#known-limitations)).

### Debug mode

Add `-debug` to any of the above to pause after every step, see its
result, and choose whether to continue or abort — or drop into a
manual shell in the job's container by answering `s`:

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
- Secondary/service containers alongside a job (GitLab's `services:`,
  CircleCI's additional `docker:` entries beyond the first) are not
  started — only the job's primary image runs.
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
