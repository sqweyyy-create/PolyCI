package githubactions

import (
	"fmt"
	"os/exec"
	"regexp"
	"strings"

	"github.com/sqweyyy-create/PolyCI/internal/pipeline"
)

// expressionPattern matches GitHub Actions' `${{ ... }}` expression syntax
// and captures its inner text. Only a small, explicitly supported subset of
// the real expression language is evaluated — matrix.<key>, env.<KEY>,
// github.sha, and github.ref. Everything else (secrets.*, functions like
// contains(), needs.*.outputs, steps.*, etc.) is left as the literal,
// unexpanded text and reported as a Finding, never silently passed through
// or guessed at.
var expressionPattern = regexp.MustCompile(`\$\{\{(.*?)\}\}`)

var matrixExprPattern = regexp.MustCompile(`^matrix\.([A-Za-z0-9_-]+)$`)
var envExprPattern = regexp.MustCompile(`^env\.([A-Za-z0-9_]+)$`)

// gitInfo carries the facts from the local git repository at the workspace
// root that github.sha and github.ref substitute from.
type gitInfo struct {
	sha       string
	ref       string
	available bool
}

// loadGitInfo reads HEAD's commit and symbolic ref from the git repository
// at dir. available is false when dir isn't inside a git repository at
// all — substituteExpressions then falls back to an empty string and
// records a Finding rather than failing the whole parse, since a workflow
// that happens to reference github.sha/github.ref is otherwise runnable.
func loadGitInfo(dir string) gitInfo {
	sha, err := runGit(dir, "rev-parse", "HEAD")
	if err != nil {
		return gitInfo{}
	}
	// A detached HEAD (e.g. a checked-out tag) has no symbolic ref; ref is
	// left empty in that case rather than guessed at.
	ref, _ := runGit(dir, "symbolic-ref", "-q", "HEAD")
	return gitInfo{sha: sha, ref: ref, available: true}
}

func runGit(dir string, args ...string) (string, error) {
	out, err := exec.Command("git", append([]string{"-C", dir}, args...)...).Output()
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(out)), nil
}

// exprContext is everything substituteExpressions needs to resolve a
// supported expression for one job (or one matrix combination of a job).
type exprContext struct {
	matrix map[string]string
	env    map[string]string
	git    gitInfo
}

// substituteExpressions replaces every supported `${{ ... }}` expression in
// s with its value. Anything it can't resolve — an unrecognized
// context/function, or a supported context whose value genuinely isn't
// available (matrix.<key> when the job has no such key, github.sha/ref
// outside a git repo) — is left in s unexpanded, and reported via one
// returned Finding per such occurrence. job/step name where it was found.
func substituteExpressions(s, jobName, stepName string, ctx exprContext) (string, []pipeline.Finding) {
	var findings []pipeline.Finding
	out := expressionPattern.ReplaceAllStringFunc(s, func(match string) string {
		inner := strings.TrimSpace(expressionPattern.FindStringSubmatch(match)[1])

		record := func(level pipeline.FindingLevel, detail string) {
			findings = append(findings, pipeline.Finding{
				Job:     jobName,
				Step:    stepName,
				Feature: "${{ " + inner + " }}",
				Level:   level,
				Detail:  detail,
			})
		}
		// unsupported is for an expression we can't resolve at all (an
		// unrecognized context/function, or a reference to a matrix key
		// that doesn't exist) — the literal text is left in place, since
		// substituting something plausible-looking would be worse than an
		// obviously-unexpanded ${{ }} in the command.
		unsupported := func(detail string) string {
			record(pipeline.Unsupported, detail)
			return match
		}
		// degraded is for a context we do support in principle, but whose
		// real value genuinely isn't available here (github.sha/ref
		// outside a git repo) — substitutes an empty string, the same
		// fallback a real shell gives an unset variable, rather than
		// leaving the raw ${{ }} syntax for the shell to choke on.
		degraded := func(detail string) string {
			record(pipeline.Unsupported, detail)
			return ""
		}
		// supported records a successful substitution. Most fully-working
		// features never need a Finding at all, but `polyci check`'s
		// category breakdown needs positive evidence that an expression
		// was both present and resolved cleanly — the substituted text
		// left behind in the final command looks identical to text that
		// never had an expression in it at all, so without this the
		// Expressions category could never tell "used, fully supported"
		// apart from "never used".
		supported := func(detail, value string) string {
			record(pipeline.Supported, detail)
			return value
		}

		switch {
		case matrixExprPattern.MatchString(inner):
			key := matrixExprPattern.FindStringSubmatch(inner)[1]
			if v, ok := ctx.matrix[key]; ok {
				return supported("substituted from this job's strategy.matrix", v)
			}
			return unsupported(fmt.Sprintf("matrix.%s is not defined by this job's strategy.matrix", key))

		case envExprPattern.MatchString(inner):
			key := envExprPattern.FindStringSubmatch(inner)[1]
			// An env var that isn't set evaluates to empty, the same way
			// an unset shell variable would — still a fully-supported
			// resolution of the env. context, not a problem worth flagging
			// on its own.
			return supported("substituted from env: (empty if unset, same as an unset shell variable)", ctx.env[key])

		case inner == "github.sha":
			if !ctx.git.available {
				return degraded("not running inside a git repository; substituted with an empty string")
			}
			return supported("substituted from the local git repository's HEAD commit", ctx.git.sha)

		case inner == "github.ref":
			if !ctx.git.available {
				return degraded("not running inside a git repository; substituted with an empty string")
			}
			if ctx.git.ref == "" {
				return degraded("HEAD is detached (no symbolic ref); substituted with an empty string")
			}
			return supported("substituted from the local git repository's current ref", ctx.git.ref)

		default:
			return unsupported("expression syntax is not evaluated; the literal, unexpanded text is used")
		}
	})
	return out, findings
}
