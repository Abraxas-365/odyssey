// Package policycel is the policy.Engine over CEL (github.com/google/cel-go):
// the one place that knows the language. Expressions are type-checked against
// the Input's shape when they are saved, compiled once per (rule id,
// version), and evaluated with a bounded cost and time (docs/policy.md).
package policycel

import (
	"context"
	"fmt"
	"reflect"
	"strings"
	"sync"
	"time"

	"github.com/Abraxas-365/odyssey/internal/policy"
	"github.com/google/cel-go/cel"
	"github.com/google/cel-go/ext"
)

// Limits on what a rule may cost: an expression is a few comparisons, so
// these are generous, and a pathological one (nested comprehensions over
// the permission list) is stopped, never run to completion.
const (
	// DefaultCostLimit is the CEL cost budget of one evaluation.
	DefaultCostLimit = 10_000
	// DefaultTimeout bounds one evaluation's wall time.
	DefaultTimeout = 100 * time.Millisecond
	// maxCache bounds the compiled programs kept; the cache is cleared when
	// it fills (rules are few, so this only happens with heavy editing).
	maxCache = 2048
)

// Options tune the engine; zero values take the defaults.
type Options struct {
	CostLimit uint64
	Timeout   time.Duration
}

// The CEL-visible shape of Input. Struct tags are the CEL field names.
// NativeTypes names them "policycel.<Struct>" (see the declarations below).
type (
	projectVars struct {
		Tier string `cel:"tier"`
		Slug string `cel:"slug"`
	}
	envVars struct {
		Name string `cel:"name"`
	}
	accessVars struct {
		Mode string `cel:"mode"`
	}
	actorVars struct {
		ID          string   `cel:"id"`
		Permissions []string `cel:"permissions"`
	}
	resourceVars struct {
		Kind string `cel:"kind"`
	}
	scanVars struct {
		Known    bool  `cel:"known"`
		Checked  bool  `cel:"checked"`
		Critical int64 `cel:"critical"`
		High     int64 `cel:"high"`
	}
)

type Engine struct {
	env  *cel.Env
	opts Options

	mu    sync.Mutex
	cache map[cacheKey]cel.Program
}

type cacheKey struct {
	rule    policy.RuleID
	version int
}

var _ policy.Engine = (*Engine)(nil)

// New builds the engine; it panics when the environment cannot be declared (a
// programming error: the declarations are static).
func New(opts Options) *Engine {
	if opts.CostLimit == 0 {
		opts.CostLimit = DefaultCostLimit
	}
	if opts.Timeout <= 0 {
		opts.Timeout = DefaultTimeout
	}
	env, err := cel.NewEnv(
		ext.NativeTypes(ext.ParseStructTags(true),
			reflect.TypeOf(projectVars{}), reflect.TypeOf(envVars{}), reflect.TypeOf(accessVars{}),
			reflect.TypeOf(actorVars{}), reflect.TypeOf(resourceVars{}), reflect.TypeOf(scanVars{})),
		cel.Variable("action", cel.StringType),
		cel.Variable("project", cel.ObjectType("policycel.projectVars")),
		cel.Variable("env", cel.ObjectType("policycel.envVars")),
		cel.Variable("access", cel.ObjectType("policycel.accessVars")),
		cel.Variable("actor", cel.ObjectType("policycel.actorVars")),
		cel.Variable("resource", cel.ObjectType("policycel.resourceVars")),
		cel.Variable("scan", cel.ObjectType("policycel.scanVars")),
		cel.ParserExpressionSizeLimit(policy.MaxExpression),
		cel.ParserRecursionLimit(32),
	)
	if err != nil {
		panic(fmt.Sprintf("policycel: environment: %v", err))
	}
	return &Engine{env: env, opts: opts, cache: map[cacheKey]cel.Program{}}
}

func (e *Engine) Compile(expression string) error {
	_, err := e.check(expression)
	return err
}

// check compiles and type-checks; the result is a bool expression.
func (e *Engine) check(expression string) (*cel.Ast, error) {
	ast, issues := e.env.Compile(expression)
	if issues != nil && issues.Err() != nil {
		return nil, &policy.InvalidExpression{Message: message(issues.Err())}
	}
	if !ast.OutputType().IsExactType(cel.BoolType) {
		return nil, &policy.InvalidExpression{Message: "la expresión debe dar verdadero o falso (bool), y da " + ast.OutputType().String()}
	}
	return ast, nil
}

// message is the compiler's text without the file/position prefix it adds
// to a one-line expression ("ERROR: <input>:1:5: ...").
func message(err error) string {
	return strings.TrimSpace(strings.ReplaceAll(err.Error(), "ERROR: <input>:", "línea "))
}

func (e *Engine) Matches(ctx context.Context, rule policy.Rule, input policy.Input) (bool, error) {
	prog, err := e.program(rule)
	if err != nil {
		return false, err
	}
	ctx, cancel := context.WithTimeout(ctx, e.opts.Timeout)
	defer cancel()
	out, _, err := prog.ContextEval(ctx, activation(input))
	if err != nil {
		return false, err
	}
	matched, ok := out.Value().(bool)
	if !ok {
		return false, fmt.Errorf("la expresión no dio verdadero o falso")
	}
	return matched, nil
}

// program is the rule's compiled program, from the cache by (id, version):
// a saved edit bumps the version, so a stale program is never used. An
// unsaved rule (version 0: a dry-run candidate) is compiled every time and
// never cached, so dry runs cannot crowd the saved rules out of the cache.
func (e *Engine) program(rule policy.Rule) (cel.Program, error) {
	key := cacheKey{rule.ID, rule.Version}
	saved := rule.Version > 0
	e.mu.Lock()
	prog, ok := e.cache[key]
	e.mu.Unlock()
	if ok && saved {
		return prog, nil
	}
	ast, err := e.check(rule.Expression)
	if err != nil {
		return nil, err
	}
	prog, err = e.env.Program(ast, cel.CostLimit(e.opts.CostLimit), cel.InterruptCheckFrequency(64))
	if err != nil || !saved {
		return prog, err
	}
	e.mu.Lock()
	if len(e.cache) >= maxCache {
		clear(e.cache)
	}
	e.cache[key] = prog
	e.mu.Unlock()
	return prog, nil
}

func activation(in policy.Input) map[string]any {
	perms := in.Actor.Permissions
	if perms == nil {
		perms = []string{}
	}
	return map[string]any{
		"action":   in.Action,
		"project":  projectVars{Tier: string(in.Project.Tier), Slug: in.Project.Slug},
		"env":      envVars{Name: in.Env.Name},
		"access":   accessVars{Mode: in.Access.Mode},
		"actor":    actorVars{ID: in.Actor.ID, Permissions: perms},
		"resource": resourceVars{Kind: in.Resource.Kind},
		// a known scan has been checked, whatever a dry-run input says
		"scan": scanVars{Known: in.Scan.Known, Checked: in.Scan.Checked || in.Scan.Known, Critical: int64(in.Scan.Critical), High: int64(in.Scan.High)},
	}
}
