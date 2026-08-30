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

## Known limitations

- Phase 3's shell-on-fail feature (dropping the user into an
  interactive shell in the failing container) is verified manually
  with a real TTY, not by an automated test. An automated Go test
  driving it over a pseudo-terminal (`github.com/creack/pty`) was
  attempted but hung unreliably, so it was removed rather than left
  flaky in the suite. Future changes to the attach/shell logic in
  `internal/executor/docker.go` (the `Shell` method and its TTY
  plumbing in `tty_unix.go`/`tty_windows.go`) should be manually
  re-verified against a real Docker container until a reliable
  automated test exists.
- `Shell`'s stdin-forwarding goroutine must be reliably stopped
  *before* `Shell` returns, not just abandoned — a real terminal's
  stdin never gives EOF on its own, so a leftover goroutine still
  blocked in a read would race whatever the caller reads from stdin
  next (e.g. the debugger's post-shell continue/abort/retry prompt),
  deterministically stealing that input. The fix
  (`waitStdinReadable` in `tty_unix.go`, backed by `poll(2)` via
  `golang.org/x/sys/unix`) was chosen only after two other approaches
  were tried and empirically disproven against a real pty: (1)
  `os.File.SetReadDeadline` on a duplicated stdin fd silently never
  fires — a dup wrapped in `os.NewFile` doesn't get properly
  registered with the Go runtime's poller on darwin; (2)
  `syscall.SetNonblock` on a duplicated fd looked like it worked, but
  duplicated fds share the same underlying open file description as
  the original in POSIX, *including status flags* — so it also made
  the real `os.Stdin` non-blocking, silently breaking every later
  blocking read of it (including the debugger's own prompts).
  `poll(2)` has no such side effect: it only queries readiness, never
  touches the fd's flags. Any future rewrite of this stdin-cancellation
  logic should re-verify against a real pty, not just trust that an
  approach compiles and looks right.
- Every job's container gets the current working directory (where
  `polyci run` was invoked, overridable via `executor.WithWorkspace`
  for tests) bind-mounted read-write at /workspace, so jobs run
  against your actual project files, and files a job writes are
  visible to later jobs and back on the host afterward — this is
  fixed as of the workspace-mount work; see internal/executor/
  docker_test.go's TestWorkspaceMountReadWrite. CircleCI's `checkout`
  step is therefore a no-op (there's no separate clone step to run;
  your files are already there via the mount). Other unsupported
  builtin steps (`save_cache`, `restore_cache`, `persist_to_workspace`,
  `attach_workspace`, `store_artifacts`, `store_test_results`,
  `setup_remote_docker`, `add_ssh_keys`, `deploy`) still become a
  visible no-op log line rather than erroring, so real-world configs
  still parse and run even though those specific behaviors aren't
  implemented.
- The workspace bind mount requires the Docker engine to be able to
  see the host path being mounted. Colima (the default local setup
  for this project) only shares `$HOME` into its VM by default — a
  project outside your home directory, or a host path used in a test
  (see `hostMountableTempDir` in `internal/executor/docker_test.go`,
  which deliberately avoids `t.TempDir()`'s OS temp dir for this
  reason), will fail to mount with "bind source path does not exist".
  If you hit this with a real project, either move it under $HOME or
  add its path to colima's `mounts:` config
  (`~/.colima/default/colima.yaml`) and restart colima.
- Containers run as root by default, so files a job writes into
  /workspace typically show up on the host owned by root (or by
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
  different name via `name:` in a workflow's job list.
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
  file is run per invocation (`-f` is required, since workflow files
  can be named anything under `.github/workflows/`), not every file
  in that directory.
