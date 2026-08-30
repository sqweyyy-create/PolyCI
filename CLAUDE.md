# PolyCI (working name — rename freely)

We're building PolyCI, a CLI tool that runs CI/CD pipelines locally in
Docker containers, with a debugger, across multiple CI providers —
starting where no good local tool currently exists.

## Why this project exists

GitHub Actions already has a mature local runner (`act` by nektos) with
an active community and a VS Code extension. We are NOT trying to
replace or out-compete `act` on GitHub Actions. Our real value is:

1. GitLab CI and CircleCI currently have NO equivalent local runner —
   that's our actual differentiator and where we start.
2. A step-by-step debugger and an interactive on-failure shell (like
   `action-tmate`, but built in, not a separate GitHub Action) — this
   is a genuine gap even for GitHub Actions users of `act`.
3. Eventually: one unified tool for people whose repos use more than
   one CI provider, instead of juggling separate tools per provider.

## Explicitly OUT OF SCOPE for early phases

- Do NOT build GitHub Actions support first. It's last (Phase 5),
  after GitLab CI, the debugger, and CircleCI, specifically because
  `act` already solves it well — it's the least differentiated part.
- Do NOT try to achieve 100% fidelity with every CI provider's exotic
  features on day one. Cover the common case (linear jobs, steps,
  Docker images, env vars, artifacts) before edge cases (matrix builds,
  complex caching, provider-specific integrations).

## Tech stack

- Go for the CLI — single static binary, no runtime dependency for
  end users, and it's what `act` itself is written in
- Docker Engine API (via the official Go SDK) to create/run/exec into
  containers — do not shell out to the `docker` CLI as a subprocess;
  the SDK is more reliable
- YAML parsing for each CI provider's config format
- An internal, provider-agnostic pipeline model (jobs → steps → each
  step's image/command/env) that every provider's parser converts
  into, so the Docker execution engine and the debugger are written
  ONCE and reused across all providers

## Architecture (why it matters for build order)

    [Provider-specific parser] -> [Internal pipeline model] -> [Docker executor] -> [Debugger layer]
     .gitlab-ci.yml                jobs/steps/env               run containers      pause/resume,
     (Phase 1)                     (Phase 1)                    (Phase 1)           shell-on-fail
     circleci config.yml                                                            (Phase 2-3)
     (Phase 4)
     GitHub Actions workflow
     (Phase 5)

Keeping the parser separate from the executor from day one is what
makes Phase 4 (add CircleCI) and Phase 5 (add GitHub Actions) cheap
later — a new parser plugs into the same executor and debugger.

## Build order (phases — build and verify each before starting the next)

1. **Phase 1**: Parse `.gitlab-ci.yml`, run jobs/steps in Docker
   containers in order, stream logs to the terminal. This alone is
   already useful and already differentiated (no equivalent exists).
2. **Phase 2**: Step-by-step debugger — pause after each step, show
   its result, let the user choose continue/abort.
3. **Phase 3**: On step failure, drop the user into an interactive
   shell inside the same container where the failure happened.
4. **Phase 4**: Add a CircleCI parser, reusing the same internal model
   and executor.
5. **Phase 5**: Add a GitHub Actions parser (lowest priority — `act`
   already covers this well; the value here is having one tool for
   people who use multiple CI providers).

## Conventions

- Every command should fail gracefully with a clear message (Docker
  not running, invalid YAML, missing image, etc.) — never a raw stack
  trace to the user.
- Log output should make clear which job/step is currently running.
- Prefer small, testable functions over one large command handler.
- Tests must NEVER depend on a real remote CI service or push to a
  real repository — they run against local fixture YAML files and a
  local Docker daemon only.
