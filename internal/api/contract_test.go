// Package api's contract test guards the one rule that matters most for a
// publicly consumed API: every route actually served must be documented in
// openapi.json, and every path documented in openapi.json must actually be
// served — see docs/api.md and the "API-first" rule in CLAUDE.md ("If it's
// not in the API, it doesn't exist" also has to mean "if it's not in the
// docs, it doesn't ship").
//
// Routes are extracted from RegisterRoutes' source via go/ast rather than
// reflecting into http.ServeMux's unexported internals, which differ
// across Go versions and expose no public route-listing API. Reading the
// same "GET /api/v1/..." string literals a maintainer reads by eye keeps
// this test's failure output directly actionable: it names the exact
// route string that's missing or orphaned.
package api

import (
	"encoding/json"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"testing"
)

// route is a normalized HTTP method + path pair. Path parameters are
// rewritten to a single placeholder token so "/{id}" (Go 1.22+ mux syntax)
// and "/{name}" (the more descriptive names openapi.json documents) compare
// equal — the two are allowed to use different parameter names as long as
// the shape of the route matches.
type route struct {
	method string
	path   string
}

var pathParamPattern = regexp.MustCompile(`\{[^}]+\}`)

func normalizeRoute(method, path string) route {
	return route{
		method: method,
		path:   pathParamPattern.ReplaceAllString(path, "{param}"),
	}
}

// registeredRoutes parses api.go's RegisterRoutes function and returns
// every "METHOD /path" pattern passed to mux.Handle/mux.HandleFunc as a
// string literal. Routes gated behind a runtime condition (currently only
// the OIDC login/callback pair, registered `if oidcProvider != nil`) are
// still extracted — they belong in the public API contract regardless of
// whether OIDC happens to be configured on a given deployment.
func registeredRoutes(t *testing.T) []route {
	t.Helper()

	fset := token.NewFileSet()
	src, err := os.ReadFile("api.go")
	if err != nil {
		t.Fatalf("read api.go: %v", err)
	}
	file, err := parser.ParseFile(fset, "api.go", src, 0)
	if err != nil {
		t.Fatalf("parse api.go: %v", err)
	}

	var routes []route
	ast.Inspect(file, func(n ast.Node) bool {
		call, ok := n.(*ast.CallExpr)
		if !ok {
			return true
		}
		sel, ok := call.Fun.(*ast.SelectorExpr)
		if !ok {
			return true
		}
		ident, ok := sel.X.(*ast.Ident)
		if !ok || ident.Name != "mux" {
			return true
		}
		if sel.Sel.Name != "Handle" && sel.Sel.Name != "HandleFunc" {
			return true
		}
		if len(call.Args) == 0 {
			return true
		}
		lit, ok := call.Args[0].(*ast.BasicLit)
		if !ok || lit.Kind != token.STRING {
			return true
		}
		pattern, err := stringLiteralValue(lit.Value)
		if err != nil {
			t.Fatalf("decode route literal %s: %v", lit.Value, err)
		}
		method, path, ok := strings.Cut(pattern, " ")
		if !ok {
			t.Fatalf("route pattern %q is not in \"METHOD /path\" form", pattern)
		}
		routes = append(routes, normalizeRoute(method, path))
		return true
	})

	if len(routes) == 0 {
		t.Fatal("extracted zero routes from api.go — the AST walk is likely broken, not the API")
	}
	return routes
}

func stringLiteralValue(raw string) (string, error) {
	// raw is the literal source text including its quotes (e.g. `"GET /health"`);
	// strconv.Unquote handles the escaping.
	return strconv.Unquote(raw)
}

// documentedRoutes reads openapi.json and returns one route per (path,
// operation) pair it documents.
func documentedRoutes(t *testing.T) []route {
	t.Helper()

	var spec struct {
		Paths map[string]map[string]json.RawMessage `json:"paths"`
	}
	if err := json.Unmarshal(openAPISpec, &spec); err != nil {
		t.Fatalf("parse openapi.json: %v", err)
	}

	httpMethods := map[string]bool{
		"get": true, "post": true, "put": true, "patch": true, "delete": true, "head": true, "options": true,
	}

	var routes []route
	for path, operations := range spec.Paths {
		for op := range operations {
			if !httpMethods[op] {
				continue // "parameters", "summary", etc. at the path-item level
			}
			routes = append(routes, normalizeRoute(strings.ToUpper(op), path))
		}
	}
	return routes
}

func routeSet(routes []route) map[route]bool {
	set := make(map[route]bool, len(routes))
	for _, r := range routes {
		set[r] = true
	}
	return set
}

func sortedRouteStrings(routes []route) []string {
	out := make([]string, len(routes))
	for i, r := range routes {
		out[i] = r.method + " " + r.path
	}
	sort.Strings(out)
	return out
}

// TestEveryRouteIsDocumented is the direction that protects users of the
// public API: an endpoint DoUpRo actually serves but never wrote down.
func TestEveryRouteIsDocumented(t *testing.T) {
	registered := routeSet(registeredRoutes(t))
	documented := routeSet(documentedRoutes(t))

	var undocumented []route
	for r := range registered {
		if !documented[r] {
			undocumented = append(undocumented, r)
		}
	}
	if len(undocumented) > 0 {
		t.Fatalf("route(s) registered in RegisterRoutes but missing from openapi.json:\n  %s\n"+
			"add them to internal/api/openapi.json (see docs/api.md)",
			strings.Join(sortedRouteStrings(undocumented), "\n  "))
	}
}

// TestEveryDocumentedRouteExists is the direction that protects DoUpRo's
// own credibility: a promise in openapi.json (and therefore in Swagger UI)
// that nothing actually serves — stale docs left behind after a handler
// was renamed or removed.
func TestEveryDocumentedRouteExists(t *testing.T) {
	registered := routeSet(registeredRoutes(t))
	documented := routeSet(documentedRoutes(t))

	var orphaned []route
	for r := range documented {
		if !registered[r] {
			orphaned = append(orphaned, r)
		}
	}
	if len(orphaned) > 0 {
		t.Fatalf("route(s) documented in openapi.json but not registered in RegisterRoutes:\n  %s\n"+
			"either implement them or remove them from internal/api/openapi.json",
			strings.Join(sortedRouteStrings(orphaned), "\n  "))
	}
}
