# Real-World Compatibility Report

This is a validation pass against real, unmodified CI config files pulled
from public open-source repositories — not PolyCI's own fixtures. The goal
is an honest map of where PolyCI's parsers and executor actually stand
against configs nobody wrote with PolyCI in mind, not a marketing claim.
Where reality diverges from what's supported, that's exactly what this
document is for.

**Tested against:** PolyCI `v0.2.0` (commit `3aa2fa9`), 2026-08-30.

> **Update (2026-08-31):** the two `fdroidclient` divergences below —
> `extends:` being silently ignored, and a real `pages:` job being
> silently dropped — have since been fixed in `internal/gitlab/parser.go`.
> `reservedTopLevelKeys` now lists only the 10 keywords that are
> genuinely top-level in real GitLab CI (`pages` was never one of
> them), so a job named `pages` parses like any other job. `extends:`
> is now resolved one level deep (`variables:` deep-merged, everything
> else child-wins), which is enough to correctly handle both of
> `fdroidclient`'s patterns — `deploy_nightly`'s direct `extends:
> .base`, and the anchor-merged jobs' indirect `extends:` via
> `.test-template` (verified by `TestParseExtendsThroughAnchorMerge` in
> `internal/gitlab/parser_test.go`, which reproduces this exact
> pattern). A single-level-only limitation remains for chains deeper
> than that, which `polyci check` now reports explicitly instead of
> silently under-merging — see the new `polyci check` command in
> [`README.md`](./README.md#checking-compatibility). The details below
> are left as originally written to preserve the record of what was
> found and how; they no longer reflect current behavior where noted.

> **Update (2026-08-31): partial job execution.** All 6 "Failed to
> parse" GitHub Actions files below, plus both CircleCI orb/executor
> ones, failed because ONE unsupported job aborted parsing of the
> *entire* file — even though every other job in the same file was
> perfectly runnable. That's now fixed: a job PolyCI can't run at all
> (no `container:`, a non-`docker:` CircleCI executor, an orb-provided
> job CircleCI-workflow references but never defines) is individually
> skipped — recorded in `Pipeline.SkippedJobs` with a clear reason, and
> printed by both `polyci run` and `polyci check` — while every other
> job in the file still runs normally. A job that `needs:`/`requires:`
> a skipped job is itself skipped too (cascading, via the new
> `dag.FilterSkipped`), rather than crashing on an unresolvable
> dependency. Re-running every file below through `polyci check`
> against the *current* PolyCI (re-fetching each file fresh, so a few
> counts differ slightly from the original pass below where the
> project's default branch has since changed) gives:
>
> | File | Before | After |
> |---|---|---|
> | `redis/redis` | ❌ Failed to parse | ✅ 3 runnable, 5 skipped (no `container:`) |
> | `prometheus/prometheus` | ❌ Failed to parse | ✅ 7 runnable, 15 skipped (no `container:`, some cascaded) |
> | `CircleCI-Public/circleci-demo-python-flask` | ❌ Failed to parse | ✅ 1 runnable (`build`), 1 skipped (`deploy`'s orb executor) |
> | `sindresorhus/execa` | ❌ Failed to parse | ⚠️ 0 runnable, 1 skipped — its only job has no `container:`, so there's nothing to partially succeed *at*, but it's now a clean report instead of a crash |
> | `psf/requests` | ❌ Failed to parse | ⚠️ 0 runnable, 3 skipped (same shape as `execa` — no job in the file has `container:`) |
> | `gin-gonic/gin` | ❌ Failed to parse | ⚠️ 0 runnable, 2 skipped (same shape) |
> | `vitejs/vite` | ❌ Failed to parse | ⚠️ 0 runnable, 5 skipped (same shape) |
> | `babel/babel` | ❌ Failed to parse | ⚠️ 0 runnable, 1 skipped — its current `.circleci/config.yml` now declares only one job (`build-standalone`), which uses a named `executors:` block instead of inline `docker:` |
>
> Two files stay ❌ **unaffected**, since their cause has nothing to do
> with per-job execution: `gitlab.com/gitlab-org/gitlab-runner` (every
> real job lives behind `include:`, so the top level has no job-shaped
> entries at all to even attempt skipping) and
> `gitlab.com/inkscape/inkscape` (a duplicate-YAML-key decode error,
> before any job is ever reached). `gitlab.gnome.org/GNOME/gimp` now
> technically returns success (0 runnable, 1 job named `spec` skipped
> for "no image specified") instead of erroring — but this is a
> parser-edge-case artifact, not a real fix: `gopkg.in/yaml.v3`'s
> `Unmarshal` only ever decodes a multi-document YAML file's *first*
> document, which for this file is just the CI/CD Components
> `spec:`/`inputs:` block; the real jobs (in the second document) are
> still never reached. "1 job skipped" here refers to that spurious
> `spec:` entry being misidentified as a job, not to anything a real
> GitLab pipeline would call a job — multi-document YAML support
> remains a genuine gap, not something partial execution touches.

**Method:** for each file, only the config file itself was downloaded (not
the full source repository, except where noted) into an empty directory,
then `polyci plan` was run (safe, parses only, no Docker), and `polyci
run` was run if `plan` succeeded. Every outcome below reflects an actual
command run against the actual file — none of this is inferred from
reading the YAML alone, though a few `run` attempts were skipped where
actually executing would have been impractical (explained inline).

Three outcomes are possible for each file:
- ✅ **Parsed and ran** — either fully to completion, or as far as
  reasonably possible given that only the config file (not the full repo)
  was fetched.
- ⚠️ **Parsed, but diverges** — PolyCI accepted the file without error,
  but silently ignores a feature the job actually depends on, so a real
  run would not behave like real CI.
- ❌ **Failed to parse** — `polyci plan` errored outright.

## GitLab CI

| Source | Outcome | Cause |
|---|---|---|
| [`gitlab.com/gitlab-examples/ci-debug-trace`](https://gitlab.com/gitlab-examples/ci-debug-trace/-/blob/master/.gitlab-ci.yml) | ✅ Parsed and ran | — single trivial job, no unsupported features |
| [`gitlab.com/fdroid/fdroidclient`](https://gitlab.com/fdroid/fdroidclient/-/blob/master/.gitlab-ci.yml) | ⚠️ Parsed, diverges *(both causes fixed as of 2026-08-31 — see Update note above)* | `extends:` is silently ignored; a real job named `pages` is silently dropped entirely |
| [`gitlab.com/inkscape/inkscape`](https://gitlab.com/inkscape/inkscape/-/blob/master/.gitlab-ci.yml) | ❌ Failed to parse | Duplicate YAML mapping keys (three `if:` under one `rules:` entry) |
| [`gitlab.com/gitlab-org/gitlab-runner`](https://gitlab.com/gitlab-org/gitlab-runner/-/blob/main/.gitlab-ci.yml) | ❌ Failed to parse | Every real job lives in `include:`d files; `include:` isn't supported |
| [`gitlab.gnome.org/GNOME/gimp`](https://gitlab.gnome.org/GNOME/gimp/-/blob/master/.gitlab-ci.yml) | ❌ Failed to parse *(now returns success but for a spurious reason — see partial-execution Update note above)* | Top-level `spec:`/`inputs:` (CI/CD Components) block, plus the file is multi-document YAML |

### Details

**`ci-debug-trace` — clean pass.** One job (`debug_trace`), an `alpine`
image, one `echo`. `polyci plan` showed it correctly; `polyci run`
executed it and printed the expected output. Nothing noteworthy — this
is exactly PolyCI's intended common case.

**`fdroidclient` — two distinct, both-quiet divergences.** `polyci plan`
parsed 13 jobs and produced a correct-looking dependency plan (`lint`
stage jobs in parallel → `test` stage jobs in parallel, each depending
on all of `lint` → `deploy_nightly`). Tracing exactly *how* those 13
jobs get their configuration turned up two separate, unrelated gaps —
both invisible in `plan`'s output, only surfacing on an actual run:

1. **`extends:` isn't resolved, and it matters even when jobs don't use
   it directly.** Most of the real test jobs (`app assembleRelease
   test`, `libs db test`, and others) don't write `extends:` themselves
   — they merge in a hidden template via plain YAML anchor syntax,
   `<<: *test-template`. That part works correctly, because merge-key
   resolution is a native YAML feature `gopkg.in/yaml.v3` handles during
   decoding, before PolyCI's own code ever sees the result. The problem
   is one layer up: `.test-template` *itself* is defined as `extends:
   .base`, where `.base` provides `tags:`, `variables:` (`JAVA_HOME`,
   `GRADLE_USER_HOME`, ...), a `before_script:` that installs the
   Android SDK, and an `after_script:`. `extends:` is GitLab-specific
   semantics layered on top of YAML, not a YAML feature itself, and
   `internal/gitlab/parser.go` never reads it at all. So every job built
   from `.test-template` parses fine and keeps its own `script:` — but
   runs without the Android SDK `before_script:` that `script:` actually
   depends on. `deploy_nightly` hits the same gap even more directly: it
   writes `extends: .base` on itself, with no anchor involved at all.
2. **A real job named `pages` is silently dropped.** The file also
   defines a `pages:` job — a real job, with its own `script:`, that
   builds and publishes API docs — which doesn't appear anywhere in
   `polyci plan`'s 13-job output at all, and produces no error either.
   The cause: `internal/gitlab/parser.go`'s `reservedTopLevelKeys` map
   includes `"pages"`, treating it as a reserved top-level GitLab
   *keyword* to skip unconditionally. But `pages` is really just a
   *magic job name* in real GitLab CI (GitLab specially publishes
   whatever it produces as artifacts) — the job itself is meant to run
   like any other. Any repo that defines a `pages:` job — a common
   pattern — has that job silently vanish under PolyCI today, with no
   error to indicate it happened.

Given the size of the job images (a custom `fdroidserver` buildserver
image) and that several jobs perform real Android SDK downloads and
network calls not meant for arbitrary local execution, an actual `polyci
run` of this file was not attempted — the static analysis above is
conclusive enough (the `before_script:` that would set up the build
environment is provably never read, and `pages` is provably never even
considered a job) without needing to watch a slow, resource-heavy
failure play out.

Neither gap was mentioned in README's Known Limitations at the time this
was written; both are fixed now — see the Update note above.

**`inkscape` — real YAML, strictly invalid.** `polyci plan` failed with:

```
Error: parsing .gitlab-ci.yml: job "inkscape-signed-installers": yaml: unmarshal errors:
  line 341: mapping key "if" already defined at line 340
  line 342: mapping key "if" already defined at line 340
  line 342: mapping key "if" already defined at line 341
```

The upstream file genuinely has:

```yaml
rules:
  - if: $CI_CERT_CERTIFICATE
    if: $CI_CERT_KEY
    if: $CI_CERT_PASSWORD
    when: manual
```

Three `if:` keys in one mapping — this is a duplicate key, which the
[YAML spec](https://yaml.org/spec/1.2.2/#3211-nodes) says is an error,
but which many YAML libraries (including Ruby's Psych, which is what
GitLab's own config validator uses) tolerate by silently keeping the
last value. `gopkg.in/yaml.v3` — what this project uses — rejects it
outright. This isn't a missing PolyCI feature so much as a strictness
mismatch: the file works on real GitLab CI because GitLab's parser is
more forgiving of malformed YAML than ours is. See also `yarn` below —
the same pattern shows up in a completely unrelated real CircleCI
config, which suggests this is a systemic gap worth taking seriously
rather than a one-off. Not currently mentioned in Known Limitations.

**`gitlab-runner` — entirely `include:`-driven.** `polyci plan` failed
with `no runnable jobs found in config`. The fetched `.gitlab-ci.yml` is
genuinely just a `stages:` list plus 15 `include: - local: ...` entries
(plus a `component:` and a `project:` include) — literally zero jobs are
defined in the file itself. PolyCI's GitLab parser doesn't fetch or
expand `include:` at all, so from its point of view this file defines no
jobs, which is exactly the error it gave. This is likely common for any
larger GitLab project that splits its pipeline across multiple files
(a very standard practice for exactly the projects most worth testing
against). Not currently mentioned in Known Limitations.

**`gimp` — CI/CD Components format, plus multi-document YAML.** `polyci
plan` failed with:

```
Error: parsing .gitlab-ci.yml: job "spec": no image specified (set image:, default.image, or top-level image:)
```

Two compounding issues. First, the file opens with a `spec:` /
`inputs:` block — GitLab's newer "CI/CD Components" input-declaration
format — which PolyCI's parser has no awareness of and, since `spec` is
not a reserved top-level key, mistakes for a job definition. Second, and
more fundamentally: the file is multi-document YAML (a `---` separator
splits the `spec:` block from the actual pipeline body that follows).
`gopkg.in/yaml.v3`'s `Unmarshal` into a single node only decodes the
*first* document in a stream — so even setting the `spec:` misparse
aside, everything after the `---` (the real `include:` and job
definitions) is never read at all. The error message names `spec`
specifically because that's the only "job" found in that first document.
Not currently mentioned in Known Limitations.

## CircleCI

| Source | Outcome | Cause |
|---|---|---|
| [`CircleCI-Public/circleci-demo-python-flask`](https://github.com/CircleCI-Public/circleci-demo-python-flask/blob/master/.circleci/config.yml) | ❌ Failed to parse *(now partially succeeds — see partial-execution Update note above)* | `deploy` job uses an orb-provided `executor: heroku/default` |
| [`babel/babel`](https://github.com/babel/babel/blob/main/.circleci/config.yml) | ❌ Failed to parse *(now reports cleanly — see partial-execution Update note above)* | Jobs reference a named, locally-defined `executors:` block instead of inline `docker:` |
| [`scikit-learn/scikit-learn`](https://github.com/scikit-learn/scikit-learn/blob/main/.circleci/config.yml) | ✅ Parsed and ran (as far as possible) | — ran correctly up to needing real repo files |
| [`yarnpkg/yarn`](https://github.com/yarnpkg/yarn/blob/master/.circleci/config.yml) | ❌ Failed to parse | Duplicate YAML merge keys (`<<: *a` then `<<: *b` in one mapping) |

### Details

**`circleci-demo-python-flask` — orb-provided executor.** Failed with
`job "deploy": only the docker executor is supported (job has no docker:
key)`. The job in question is:

```yaml
deploy:
  executor: heroku/default
  steps: [...]
```

`heroku/default` is an executor provided by the `heroku` orb, referenced
by name rather than declared as a `docker:` list. README's Known
Limitations already says the CircleCI parser "doesn't expand `orbs:`" —
this is exactly that gap, now confirmed against a real config rather
than just stated abstractly. Every *other* job in this same small demo
file uses a plain `docker:` list and would very likely parse and run
fine on its own; the failure is specifically this one orb-executor job,
but since parsing is all-or-nothing per file, one unsupported job blocks
the whole config.

**`babel` — named, reusable `executors:` block.** Failed with `job
"build-standalone": only the docker executor is supported (job has no
docker: key)`. Unlike the Flask example, this isn't an orb — babel
defines its own executor once, at the top level:

```yaml
executors:
  node-executor:
    docker:
      - image: cimg/node:current
    working_directory: ~/babel
```

...and jobs reference it by name (`executor: node-executor`) instead of
repeating the `docker:` list inline. The underlying image is perfectly
usable — `cimg/node:current` — PolyCI's parser just never looks at the
top-level `executors:` block or resolves `executor:` references to it,
only `job.docker:` directly. This is a distinct gap from orb executors
(no orb involved at all, purely CircleCI's own executor-reuse feature)
and isn't currently called out in Known Limitations, which only
mentions unsupported *executor types* (`machine`, `macos`, `windows`),
not this indirection mechanism.

**`scikit-learn` — clean parse, expected-limitation run.** `polyci plan`
produced a correct 3-level dependency plan (`lint` → `doc`/
`doc-min-dependencies` in parallel → `deploy`) from real `requires:`
edges. `polyci run` correctly executed `checkout` as its documented
no-op, then failed on:

```
[lint] ERROR: Could not open requirements file: [Errno 2] No such file or directory: 'build_tools/github/lint_lock.txt'
```

This is not a new finding — it's the already-documented "no real
checkout, jobs run against whatever the workspace mount actually
contains" limitation from CLAUDE.md/README, now observed concretely:
since only `config.yml` was fetched (not scikit-learn's full source
tree), the file the `lint` job's `pip install -r ...` step needs
genuinely isn't present. A full `git clone` of scikit-learn was not
attempted for this report (a large repo, and the resulting failure mode
would very likely just be missing system/Python dependencies unrelated
to PolyCI's own correctness) — but the *parsing and scheduling* half of
this result is a clean, real success.

**`yarn` — duplicate YAML merge keys.** Failed with `job "install": yaml:
unmarshal errors: line 91: mapping key "<<" already defined at line 90`:

```yaml
jobs:
  install:
    <<: *docker_defaults
    <<: *install_steps
```

Two `<<:` merge keys in the same mapping — the same duplicate-key
strictness issue as `inkscape` above, in a completely different
provider's config, written by a completely different team. Two
independent real-world hits on the same root cause is enough to call
this a pattern rather than a fluke: whatever YAML CircleCI's and
GitLab's own parsers use is evidently more permissive about duplicate
keys (including duplicate merge keys) than `gopkg.in/yaml.v3`'s default
`Unmarshal` behavior.

## GitHub Actions

| Source | Outcome | Cause |
|---|---|---|
| [`sindresorhus/execa`](https://github.com/sindresorhus/execa/blob/main/.github/workflows/main.yml) | ❌ Failed to parse *(now reports cleanly — see partial-execution Update note above)* | No `container:` (matrix over OS + Node version) |
| [`psf/requests`](https://github.com/psf/requests/blob/main/.github/workflows/run-tests.yml) | ❌ Failed to parse *(now reports cleanly — see partial-execution Update note above)* | No `container:` |
| [`gin-gonic/gin`](https://github.com/gin-gonic/gin/blob/master/.github/workflows/gin.yml) | ❌ Failed to parse *(now reports cleanly — see partial-execution Update note above)* | No `container:` (also uses real `needs:`, matrix) |
| [`vitejs/vite`](https://github.com/vitejs/vite/blob/main/.github/workflows/ci.yml) | ❌ Failed to parse *(now reports cleanly — see partial-execution Update note above)* | No `container:` |
| [`redis/redis`](https://github.com/redis/redis/blob/unstable/.github/workflows/ci.yml) | ❌ Failed to parse *(now partially succeeds — see partial-execution Update note above)* | 3 of 8 jobs use `container:`, the other 5 don't — one blocks the whole file |
| [`prometheus/prometheus`](https://github.com/prometheus/prometheus/blob/main/.github/workflows/ci.yml) | ❌ Failed to parse *(now partially succeeds — see partial-execution Update note above)* | 9 of 22 jobs use `container:`, the other 13 don't |

### Details

**Six for six.** Every single real-world GitHub Actions workflow tested
for this report failed to parse, and for the same documented reason:
`no container: specified`. This isn't six independent bugs — it's one
limitation (already stated plainly in Known Limitations: "only jobs
that declare an explicit `container:` ... a job without `container:`
fails to parse with a clear error") landing on real code six times in a
row, because the limitation's premise — that most real workflows don't
use `container:` at all — is true. `execa`, `requests`, `gin`, and
`vite` don't use `container:` anywhere; `redis` and `prometheus` *do*
use it, but only on some jobs (3 of 8, and 9 of 22, respectively) — and
because PolyCI parses a whole file as one unit, a single job without
`container:` anywhere in the file is enough to block every job in it,
including the ones that would otherwise work fine.

This makes GitHub Actions by a wide margin the weakest-covered provider
in this report. It's also the least surprising result: CLAUDE.md and
README are both explicit that this constraint is a deliberate scope
decision (GitHub Actions is the lowest-priority provider, since `act`
already covers the "no container" case well), not an oversight — but
it's worth stating plainly that in this sample, it means **zero out of
six** real GitHub Actions workflows can be run by PolyCI today. If
GitHub Actions coverage becomes a priority, the highest-leverage change
found in this report isn't approximating `runs-on:` with a Docker image
(explicitly out of scope) — it's letting a file with a *mix* of
container and non-container jobs still run the ones that qualify,
instead of failing the whole file over one job that doesn't.

`gin-gonic/gin`'s workflow was also notable for having a real `needs:`
edge (`test: needs: lint`) and a three-axis test matrix (OS × Go version
× build tags) — exactly the kind of file that would have made a good
demonstration of PolyCI's parallel-execution and dependency-level
features in a real-world example, if `container:` coverage allowed it
to parse at all.

## Cross-cutting patterns

Two things showed up more than once, in unrelated files from unrelated
providers, which makes them worth calling out on their own:

1. **YAML strictness.** `inkscape` (GitLab, duplicate `if:` keys) and
   `yarn` (CircleCI, duplicate `<<:` merge keys) both fail for the same
   underlying reason: `gopkg.in/yaml.v3`'s default `Unmarshal` rejects
   duplicate mapping keys outright, while the actual CI providers'
   config validators are evidently more forgiving. Both files are, in a
   strict reading of the YAML spec, invalid — but they run on real CI
   today, so from a user's perspective this reads as "PolyCI rejected a
   file GitLab/CircleCI accepts," not "this YAML is malformed." Neither
   occurrence is currently mentioned in Known Limitations.
2. **All-or-nothing per-file parsing.** `redis` and `prometheus` (GitHub
   Actions) show this most starkly — files that are *mostly* usable
   (some jobs fully supported) still fail entirely because of one
   unsupported job elsewhere in the same file. The same shape of problem
   is implicit in `circleci-demo-python-flask` (one orb-executor job
   blocks an otherwise-plain-`docker:` file) and even, structurally, in
   `gitlab-runner` (every job lives behind `include:`, so there's
   nothing to partially succeed on). None of the three parsers currently
   have a way to report "N jobs parsed, M skipped for reason X" instead
   of just erroring on the first job that doesn't fit.

## Summary

| Provider | Sampled | Parsed cleanly | Parsed but diverges | Failed to parse |
|---|---|---|---|---|
| GitLab CI | 5 | 1 | 1 | 3 |
| CircleCI | 4 | 1 | 0 | 3 |
| GitHub Actions | 6 | 0 | 0 | 6 |

Fifteen files, real and unmodified, from fifteen different projects. One
(`ci-debug-trace`) ran fully end-to-end. A second (`scikit-learn`)
parsed and correctly scheduled real dependency edges from `requires:`,
failing only on a missing test-fixture file — an artifact of this
report only fetching the config file rather than the whole repo, not a
PolyCI defect. Every other outcome traces to one of seven concrete,
nameable causes: `extends:`, `include:`, an unconditionally-reserved
`pages:` job name, GitLab CI/CD Components + multi-document YAML,
CircleCI orb/named executors, YAML duplicate-key strictness, or GitHub
Actions' `container:` requirement (compounded by all-or-nothing
per-file parsing). This is not a random or vague set of gaps — the same
short list of causes accounts for every divergence found, which is a
reasonable starting point for prioritizing what to close first. (As of
2026-08-31, two of these seven — `extends:` and the reserved `pages:`
job name — are fixed; see the Update note under "Tested against"
above. `include:` remains unimplemented, but is no longer silent: the
parser now records it as an Unsupported finding, visible via the new
`polyci check` command.)

**Updated summary, after partial job execution (2026-08-31):** the
table above is left as originally measured, since "Failed to parse"
was a true, meaningful category at the time. It no longer is, for
GitHub Actions specifically — "no `container:`" was always a
per-*job* problem, never a per-*file* one, so once an unsupported job
stopped taking the whole file down with it, every GitHub Actions
"Failed to parse" outcome in this report changed:

| Provider | Sampled | Fully/partially runnable | Diverges | Failed to parse |
|---|---|---|---|---|
| GitLab CI | 5 | 1 | 1 | 3 *(unchanged — none of the 3 failures here were a per-job problem)* |
| CircleCI | 4 | 2 *(`circleci-demo-python-flask` gained a runnable `build` job)* | 0 | 2 *(`babel/babel`, `yarnpkg/yarn` — YAML-decode failures, unaffected)* |
| GitHub Actions | 6 | 2 *(`redis/redis`, `prometheus/prometheus` now genuinely run some jobs)* | 0 | 0 — down from 6; the other 4 (`execa`, `requests`, `gin`, `vite`) now parse and report cleanly with 0 runnable jobs, since literally every job in those specific files lacks `container:` — an honest zero-job report, not a crash, but still not "runnable" |

The remaining GitHub Actions "0 runnable" files aren't a partial-
execution gap — they're files where *every* job happens to need the
one thing (`container:`) this whole tool is built around not
approximating (see [`README.md`](./README.md#known-limitations)).
Partial execution can't manufacture a runnable job that isn't there;
what it fixes is one bad job no longer being able to hide three good
ones. See the partial-job-execution Update note near the top of this
document for the full before/after per file.

This report reflects one sampling pass on one day against whatever these
repositories' default branches contained at the time — it's a snapshot,
not a guarantee, and re-running it later against the same files (or a
different sample) could reasonably turn up different results as these
projects' configs evolve.
