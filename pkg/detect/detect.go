// Package detect proposes the services of an app from the paths of its code
// (a repository's tree, a dropped folder): which folders are apps, what to
// call them, which path of the app's address each one answers. It reads
// only names, never contents, and decides nothing: a person or an assistant
// confirms the proposal (the platform is the source of truth). Both the
// control plane and tools may run it, so it imports only the standard
// library.
package detect

import (
	"path"
	"slices"
	"strconv"
	"strings"
)

// Limits of a proposal.
const (
	// MaxPaths bounds the paths read (a repository's tree, a folder).
	MaxPaths = 20000
	// MaxServices is the most services proposed (an app's limit).
	MaxServices = 8
	// MaxDepth is how deep under the root a service's folder may be.
	MaxDepth = 3
	// maxName bounds a service's name (the platform's rule).
	maxName = 20
)

// Kinds of service (the platform's words).
const (
	Web    = "web"
	Worker = "worker"
)

// markers name the files that make a folder an app, in the order an
// evidence is reported.
var markers = []string{
	"Dockerfile", "package.json", "go.mod", "pyproject.toml", "requirements.txt", "Pipfile",
	"Gemfile", "composer.json", "pom.xml", "build.gradle", "build.gradle.kts", "Cargo.toml",
	"deno.json", "mix.exs",
}

// skipped folders are never apps (dependencies, outputs, tooling) and
// nothing under them is read.
var skipped = map[string]bool{
	"node_modules": true, "vendor": true, "dist": true, "build": true, "out": true, "target": true,
	"coverage": true, "__pycache__": true, "venv": true, "testdata": true, "fixtures": true,
	"examples": true, "example": true, "docs": true, "test": true, "tests": true, "e2e": true,
}

// libraries are folders a workspace keeps shared code in (or client SDKs):
// their children are not apps.
var libraries = map[string]bool{"packages": true, "libs": true, "lib": true, "shared": true, "internal": true, "pkg": true,
	"tools": true, "scripts": true, "sdk": true, "sdks": true, "clients": true}

// webNames go first, at "/"; workerNames run without a port.
var (
	webNames    = []string{"web", "frontend", "front", "app", "site", "www", "ui", "client", "dashboard", "portal"}
	workerNames = []string{"worker", "workers", "jobs", "job", "queue", "consumer", "cron", "scheduler"}
)

// Service is one proposed service.
type Service struct {
	Name        string `json:"name"`
	Kind        string `json:"kind"`
	Root        string `json:"root"`
	Route       string `json:"route"`
	StripPrefix bool   `json:"strip_prefix"`
	// Evidence is the file that made the folder an app ("apps/web/package.json").
	Evidence string `json:"evidence"`
}

// Proposal is what Services found.
type Proposal struct {
	Services []Service `json:"services"`
	// Notes say in plain Spanish what the proposal left out or guessed.
	Notes []string `json:"notes"`
}

// Services proposes the services of the code whose file paths are paths
// (relative, "/"-separated; a trailing "/" marks a folder). root is the
// app's folder inside them ("" for all of it): only what is under it is
// read and the services' folders are relative to it. A folder is an app
// when it holds a marker file; folders under another app, under
// dependency/output folders or inside a workspace's library folders are
// not. With apps in folders the root is a workspace and they are the
// services (a folder that is itself a workspace — a workspace file such as
// pnpm-workspace.yaml or go.work, or apps in apps/ or services/ — gives its
// apps instead of being one); with a marker only at the root, the app is
// one service, "web", at "/". Nothing found is an empty proposal.
func Services(paths []string, root string) Proposal {
	root = strings.Trim(root, "/")
	var out Proposal
	if len(paths) > MaxPaths {
		paths = paths[:MaxPaths]
		out.Notes = append(out.Notes, "El código tiene más de "+strconv.Itoa(MaxPaths)+" archivos: se miraron los primeros.")
	}
	evidence := map[string]string{} // folder (relative to root) → its first marker
	workspaces := map[string]bool{} // folders with a workspace file
	for _, p := range paths {
		p = strings.Trim(path.Clean("/"+p), "/")
		if root != "" {
			if !strings.HasPrefix(p, root+"/") {
				continue
			}
			p = strings.TrimPrefix(p, root+"/")
		}
		dir, file := path.Split(p)
		dir = strings.TrimSuffix(dir, "/")
		if !readable(dir) {
			continue
		}
		if slices.Contains(workspaceFiles, file) {
			workspaces[dir] = true
		}
		if !slices.Contains(markers, file) {
			continue
		}
		if old, ok := evidence[dir]; !ok || markerRank(file) < markerRank(path.Base(old)) {
			evidence[dir] = path.Join(root, p)
		}
	}
	folders := outermost(evidence, workspaces)
	_, atRoot := evidence[""]
	if len(folders) == 0 && !atRoot {
		out.Notes = append(out.Notes, "No se encontró ninguna app (package.json, go.mod, Dockerfile…).")
		return out
	}
	if len(folders) == 0 { // only the root is an app
		out.Services = []Service{{Name: "web", Kind: Web, Root: "", Route: "/", Evidence: evidence[""]}}
		return out
	}
	if len(folders) > MaxServices {
		out.Notes = append(out.Notes, "Hay "+strconv.Itoa(len(folders))+" carpetas con apps; una app tiene como máximo "+
			strconv.Itoa(MaxServices)+" servicios: se propusieron las primeras.")
		folders = folders[:MaxServices]
	}
	if at, ok := evidence[""]; ok && !workspace("", folders, workspaces) {
		apps := "se propuso la app de su carpeta"
		if len(folders) > 1 {
			apps = "se propusieron las " + strconv.Itoa(len(folders)) + " apps de sus carpetas"
		}
		out.Notes = append(out.Notes, "La raíz también parece una app ("+at+") y quedó fuera: "+apps+
			". Si la raíz también se publica, agrégala como un servicio más sin carpeta; si es la única app, deja un solo servicio en / sin carpeta.")
	}
	out.Services = name(folders, evidence)
	route(out.Services)
	return out
}

// readable says whether a folder may hold an app: not too deep, not hidden,
// not under dependencies or outputs, not inside a library folder.
func readable(dir string) bool {
	if dir == "" {
		return true
	}
	parts := strings.Split(dir, "/")
	if len(parts) > MaxDepth {
		return false
	}
	for _, part := range parts {
		if strings.HasPrefix(part, ".") || strings.HasPrefix(part, "_") || skipped[part] || libraries[part] {
			return false
		}
	}
	return true
}

func markerRank(file string) int { return slices.Index(markers, file) }

// workspaceFiles mark a folder as a workspace of apps (not an app).
var workspaceFiles = []string{"pnpm-workspace.yaml", "turbo.json", "nx.json", "lerna.json", "go.work", "rush.json"}

// containers are the folders a workspace keeps its apps in.
var containers = []string{"apps", "services"}

// workspace says whether a folder with apps under it is a workspace (its
// apps are the services) rather than an app with a part inside: it holds a
// workspace file, or its inner apps live in apps/ or services/.
func workspace(dir string, dirs []string, workspaces map[string]bool) bool {
	prefix := dir + "/"
	if dir == "" {
		prefix = ""
	}
	inner := func(d string) bool { return d != dir && strings.HasPrefix(d, prefix) }
	if workspaces[dir] && slices.ContainsFunc(dirs, inner) {
		return true
	}
	return slices.ContainsFunc(dirs, func(d string) bool {
		return inner(d) && slices.ContainsFunc(containers, func(c string) bool { return strings.HasPrefix(d, prefix+c+"/") })
	})
}

// outermost are the app folders (sorted) that are not inside another one;
// the root and nested workspaces are left out (their apps count instead).
func outermost(evidence map[string]string, workspaces map[string]bool) []string {
	var dirs []string
	for d := range evidence {
		if d != "" {
			dirs = append(dirs, d)
		}
	}
	slices.Sort(dirs)
	var out []string
	for _, d := range dirs {
		inside := slices.ContainsFunc(out, func(o string) bool { return strings.HasPrefix(d, o+"/") })
		if !inside && !workspace(d, dirs, workspaces) {
			out = append(out, d)
		}
	}
	return out
}

// name gives each folder a unique service name made of its last segment.
func name(folders []string, evidence map[string]string) []Service {
	taken := map[string]bool{}
	out := make([]Service, 0, len(folders))
	for _, f := range folders {
		base := sanitize(path.Base(f))
		n := base
		for i := 2; taken[n]; i++ {
			suffix := strconv.Itoa(i)
			n = base[:min(len(base), maxName-len(suffix))] + suffix
		}
		taken[n] = true
		kind := Web
		if slices.Contains(workerNames, n) {
			kind = Worker
		}
		out = append(out, Service{Name: n, Kind: kind, Root: f, Evidence: evidence[f]})
	}
	return out
}

// sanitize makes s a service name: lowercase letters and digits, starting
// with a letter, at most 20.
func sanitize(s string) string {
	var b strings.Builder
	for _, r := range strings.ToLower(s) {
		switch {
		case r >= 'a' && r <= 'z':
			b.WriteRune(r)
		case r >= '0' && r <= '9' && b.Len() > 0:
			b.WriteRune(r)
		}
	}
	n := b.String()
	if n == "" {
		n = "app"
	}
	return n[:min(len(n), maxName)]
}

// route puts the web service that looks like the site at "/" (by name, else
// the first web one) and every other web service under "/<name>", stripped
// (it then sees the paths it would see alone). The site goes first.
func route(services []Service) {
	site := -1
	for _, want := range webNames {
		if i := slices.IndexFunc(services, func(s Service) bool { return s.Kind == Web && s.Name == want }); i >= 0 {
			site = i
			break
		}
	}
	if site < 0 {
		site = slices.IndexFunc(services, func(s Service) bool { return s.Kind == Web })
	}
	for i := range services {
		switch {
		case services[i].Kind == Worker:
		case i == site:
			services[i].Route = "/"
		default:
			services[i].Route, services[i].StripPrefix = "/"+services[i].Name, true
		}
	}
	if site > 0 {
		s := services[site]
		copy(services[1:site+1], services[:site])
		services[0] = s
	}
}
