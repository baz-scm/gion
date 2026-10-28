package deps

import (
	"context"
	"encoding/json"
	"encoding/xml"
	"fmt"
	"path"
	"regexp"
	"slices"
	"sort"
	"strings"
)

// --- npm, pnpm, yarn, bun ---

type packageJSON struct {
	Name                 string            `json:"name"`
	Dependencies         map[string]string `json:"dependencies"`
	DevDependencies      map[string]string `json:"devDependencies"`
	PeerDependencies     map[string]string `json:"peerDependencies"`
	OptionalDependencies map[string]string `json:"optionalDependencies"`
	Workspaces           json.RawMessage   `json:"workspaces"`
}

// npm follows file:, link: and portal: dependencies, and dependencies on
// packages of the enclosing workspace (by name, whatever the version spec:
// workspace:* or a plain range both resolve to the local package).
func (r *resolver) npm(ctx context.Context, f string, data []byte) ([]cand, error) {
	var pj packageJSON
	if json.Unmarshal(data, &pj) != nil {
		return nil, nil // not ours to validate
	}
	dir := path.Dir(f)
	if dir == "." {
		dir = ""
	}
	var ws map[string]string
	var out []cand
	for _, m := range []map[string]string{pj.Dependencies, pj.DevDependencies, pj.PeerDependencies, pj.OptionalDependencies} {
		for _, name := range sortedKeys(m) {
			v := m[name]
			spec := fmt.Sprintf("%q: %q", name, v)
			if rel, ok := cutAny(v, "file:", "link:", "portal:"); ok {
				out = append(out, cand{join(dir, rel), f, spec})
				continue
			}
			if ws == nil {
				var err error
				if ws, err = r.npmWorkspace(ctx, dir); err != nil {
					return nil, err
				}
			}
			if d, ok := ws[name]; ok && d != dir {
				out = append(out, cand{d, f, spec})
			}
		}
	}
	return out, nil
}

// npmWorkspace maps package names to directories for the nearest workspace
// root at or above dir (package.json "workspaces" or pnpm-workspace.yaml).
func (r *resolver) npmWorkspace(ctx context.Context, dir string) (map[string]string, error) {
	for _, a := range ancestors(dir) {
		key := "npm:" + a
		if m, ok := r.roots[key]; ok {
			if m != nil {
				return m.(map[string]string), nil
			}
			continue
		}
		patterns, err := r.npmPatterns(ctx, a)
		if err != nil {
			return nil, err
		}
		if patterns == nil {
			r.roots[key] = nil
			continue
		}
		dirs, err := r.members(ctx, a, patterns, nil)
		if err != nil {
			return nil, err
		}
		if err := r.src.Prefetch(ctx, dirs); err != nil {
			return nil, err
		}
		manifests := make([]string, len(dirs))
		for i, d := range dirs {
			manifests[i] = path.Join(d, "package.json")
		}
		if err := r.src.Prefetch(ctx, r.existing(ctx, manifests)); err != nil {
			return nil, err
		}
		names := map[string]string{}
		for i, m := range manifests {
			data, err := r.read(ctx, m)
			if err != nil {
				return nil, err
			}
			var pj packageJSON
			if data != nil && json.Unmarshal(data, &pj) == nil && pj.Name != "" {
				names[pj.Name] = dirs[i]
			}
		}
		r.roots[key] = names
		return names, nil
	}
	return map[string]string{}, nil
}

// npmPatterns returns the workspace globs declared in dir, or nil.
func (r *resolver) npmPatterns(ctx context.Context, dir string) ([]string, error) {
	data, err := r.read(ctx, path.Join(dir, "package.json"))
	if err != nil {
		return nil, err
	}
	var pj packageJSON
	if data != nil && json.Unmarshal(data, &pj) == nil && len(pj.Workspaces) > 0 {
		var list []string
		if json.Unmarshal(pj.Workspaces, &list) == nil {
			return list, nil
		}
		var obj struct {
			Packages []string `json:"packages"`
		}
		if json.Unmarshal(pj.Workspaces, &obj) == nil && obj.Packages != nil {
			return obj.Packages, nil
		}
	}
	data, err = r.read(ctx, path.Join(dir, "pnpm-workspace.yaml"))
	if err != nil || data == nil {
		return nil, err
	}
	return yamlList(data, "packages"), nil
}

// existing keeps the files that are in the repo.
func (r *resolver) existing(ctx context.Context, files []string) []string {
	var out []string
	for _, f := range files {
		if k, err := r.src.Stat(ctx, f); err == nil && k == File {
			out = append(out, f)
		}
	}
	return out
}

// --- Cargo ---

var depSections = map[string]bool{"dependencies": true, "dev-dependencies": true, "build-dependencies": true}

// cargo follows path dependencies (and [patch]/[replace] paths), and brings
// the whole workspace: cargo refuses to build a member if any other member
// is missing. workspace = true dependencies resolve through the root's
// [workspace.dependencies].
func (r *resolver) cargo(ctx context.Context, f string, data []byte) ([]cand, error) {
	dir := dirOf(f)
	var out []cand
	var inherited []string
	for _, s := range tomlSections(data) {
		table, sub := s.name, ""
		for name := range depSections {
			if rest, ok := strings.CutPrefix(s.name, name+"."); ok {
				table, sub = name, rest
			}
			if strings.HasPrefix(s.name, "target.") && strings.HasSuffix(s.name, "."+name) {
				table = name
			}
		}
		switch {
		case strings.HasPrefix(s.name, "patch.") || s.name == "replace" || (depSections[table] && sub == ""):
			for _, kv := range s.kvs {
				name, field, _ := strings.Cut(kv.key, ".")
				if p := tomlField(kv, "path"); p != "" {
					out = append(out, cand{join(dir, p), f, fmt.Sprintf("%s = { path = %q }", name, p)})
				}
				if field == "workspace" || inlineBool(kv.value, "workspace") {
					inherited = append(inherited, name)
				}
			}
		case depSections[table]:
			for _, kv := range s.kvs {
				if kv.key == "path" {
					p := firstString(kv.value)
					out = append(out, cand{join(dir, p), f, fmt.Sprintf("[%s] path = %q", s.name, p)})
				}
				if kv.key == "workspace" && strings.TrimSpace(kv.value) == "true" {
					inherited = append(inherited, sub)
				}
			}
		}
	}

	root, err := r.cargoRoot(ctx, dir)
	if err != nil || root == nil || !(dirOf(root.manifest) == dir || slices.Contains(root.members, dir)) {
		return out, err
	}
	for _, m := range root.members {
		if m != dir {
			out = append(out, cand{m, root.manifest, "workspace member"})
		}
	}
	for _, name := range inherited {
		if p, ok := root.deps[name]; ok {
			out = append(out, cand{p, root.manifest, fmt.Sprintf("[workspace.dependencies] %s = { path = ... }", name)})
		}
	}
	return out, nil
}

type cargoWorkspace struct {
	manifest string
	members  []string
	deps     map[string]string // [workspace.dependencies] name -> path
}

// cargoRoot finds the workspace dir belongs to: the nearest Cargo.toml at
// or above it with a [workspace] table.
func (r *resolver) cargoRoot(ctx context.Context, dir string) (*cargoWorkspace, error) {
	for _, a := range ancestors(dir) {
		key := "cargo:" + a
		if v, ok := r.roots[key]; ok {
			if v != nil {
				return v.(*cargoWorkspace), nil
			}
			continue
		}
		manifest := path.Join(a, "Cargo.toml")
		data, err := r.read(ctx, manifest)
		if err != nil {
			return nil, err
		}
		var include, exclude []string
		deps := map[string]string{}
		isRoot := false
		for _, s := range tomlSections(data) {
			switch s.name {
			case "workspace":
				isRoot = true
				for _, kv := range s.kvs {
					switch kv.key {
					case "members":
						include = tomlStrings(kv.value)
					case "exclude":
						exclude = tomlStrings(kv.value)
					}
				}
			case "workspace.dependencies":
				for _, kv := range s.kvs {
					name, _, _ := strings.Cut(kv.key, ".")
					if p := tomlField(kv, "path"); p != "" {
						deps[name] = join(a, p)
					}
				}
			}
		}
		if !isRoot {
			r.roots[key] = nil
			continue
		}
		dirs, err := r.members(ctx, a, include, exclude)
		if err != nil {
			return nil, err
		}
		if err := r.src.Prefetch(ctx, dirs); err != nil {
			return nil, err
		}
		ws := &cargoWorkspace{manifest: manifest, deps: deps}
		for _, d := range dirs {
			if k, err := r.src.Stat(ctx, path.Join(d, "Cargo.toml")); err == nil && k == File {
				ws.members = append(ws.members, d)
			}
		}
		r.roots[key] = ws
		return ws, nil
	}
	return nil, nil
}

// --- Go ---

// gomod follows local replace directives, and the modules of the go.work
// at or above the module, which the go command needs all of.
func (r *resolver) gomod(ctx context.Context, f string, data []byte) ([]cand, error) {
	dir := dirOf(f)
	var out []cand
	for _, d := range goDirectives(data, "replace") {
		if _, rhs, ok := strings.Cut(d, "=>"); ok {
			if fs := strings.Fields(rhs); len(fs) > 0 && isRelative(fs[0]) {
				out = append(out, cand{join(dir, fs[0]), f, "replace " + strings.TrimSpace(d)})
			}
		}
	}
	for _, a := range ancestors(dir) {
		work := path.Join(a, "go.work")
		key := "go:" + work
		if v, ok := r.roots[key]; ok {
			if v == nil {
				continue
			}
			return append(out, v.([]cand)...), nil
		}
		data, err := r.read(ctx, work)
		if err != nil {
			return nil, err
		}
		if data == nil {
			r.roots[key] = nil
			continue
		}
		var cs []cand
		for _, u := range goDirectives(data, "use") {
			if u = strings.Trim(strings.TrimSpace(u), `"`); isRelative(u) || u == "." {
				cs = append(cs, cand{join(a, u), work, "use " + u})
			}
		}
		for _, d := range goDirectives(data, "replace") {
			if _, rhs, ok := strings.Cut(d, "=>"); ok {
				if fs := strings.Fields(rhs); len(fs) > 0 && isRelative(fs[0]) {
					cs = append(cs, cand{join(a, fs[0]), work, "replace " + strings.TrimSpace(d)})
				}
			}
		}
		r.roots[key] = cs
		return append(out, cs...), nil
	}
	return out, nil
}

// goDirectives returns the arguments of every verb directive, single-line
// or in a ( ) block, with comments removed.
func goDirectives(data []byte, verb string) []string {
	var out []string
	block := false
	for _, line := range strings.Split(string(data), "\n") {
		if i := strings.Index(line, "//"); i >= 0 {
			line = line[:i]
		}
		line = strings.TrimSpace(line)
		switch {
		case block && line == ")":
			block = false
		case block:
			if line != "" {
				out = append(out, line)
			}
		case line == verb+" (" || line == verb+"(":
			block = true
		case strings.HasPrefix(line, verb+" "):
			out = append(out, strings.TrimSpace(strings.TrimPrefix(line, verb)))
		}
	}
	return out
}

func isRelative(p string) bool { return strings.HasPrefix(p, "./") || strings.HasPrefix(p, "../") }

// --- Python (uv) ---

// python follows [tool.uv.sources] paths and brings the uv workspace's
// members, which uv resolves together.
func (r *resolver) python(ctx context.Context, f string, data []byte) ([]cand, error) {
	dir := dirOf(f)
	var out []cand
	for _, s := range tomlSections(data) {
		switch {
		case s.name == "tool.uv.sources":
			for _, kv := range s.kvs {
				name, _, _ := strings.Cut(kv.key, ".")
				if p := tomlField(kv, "path"); p != "" {
					out = append(out, cand{join(dir, p), f, fmt.Sprintf("%s = { path = %q }", name, p)})
				}
			}
		case strings.HasPrefix(s.name, "tool.uv.sources."):
			for _, kv := range s.kvs {
				if kv.key == "path" {
					p := firstString(kv.value)
					out = append(out, cand{join(dir, p), f, fmt.Sprintf("[%s] path = %q", s.name, p)})
				}
			}
		}
	}
	for _, a := range ancestors(dir) {
		key := "uv:" + a
		v, ok := r.roots[key]
		if !ok {
			manifest := path.Join(a, "pyproject.toml")
			data, err := r.read(ctx, manifest)
			if err != nil {
				return nil, err
			}
			var include, exclude []string
			isRoot := false
			for _, s := range tomlSections(data) {
				if s.name != "tool.uv.workspace" {
					continue
				}
				isRoot = true
				for _, kv := range s.kvs {
					switch kv.key {
					case "members":
						include = tomlStrings(kv.value)
					case "exclude":
						exclude = tomlStrings(kv.value)
					}
				}
			}
			if isRoot {
				dirs, err := r.members(ctx, a, include, exclude)
				if err != nil {
					return nil, err
				}
				if err := r.src.Prefetch(ctx, dirs); err != nil {
					return nil, err
				}
				var cs []cand
				for _, d := range dirs {
					if k, err := r.src.Stat(ctx, path.Join(d, "pyproject.toml")); err == nil && k == File {
						cs = append(cs, cand{d, manifest, "uv workspace member"})
					}
				}
				v = cs
			}
			r.roots[key] = v
		}
		if v != nil {
			cs := v.([]cand)
			member := a == dir
			for _, c := range cs {
				member = member || c.path == dir
			}
			for _, c := range cs {
				if member && c.path != dir {
					out = append(out, c)
				}
			}
			break
		}
	}
	return out, nil
}

// --- Maven ---

type pom struct {
	ArtifactID string `xml:"artifactId"`
	Parent     *struct {
		RelativePath *string `xml:"relativePath"`
	} `xml:"parent"`
	Modules  []string `xml:"modules>module"`
	Profiles []struct {
		Modules []string `xml:"modules>module"`
	} `xml:"profiles>profile"`
	Dependencies []struct {
		ArtifactID string `xml:"artifactId"`
	} `xml:"dependencies>dependency"`
}

func parsePOM(data []byte) (*pom, bool) {
	var p pom
	if data == nil || xml.Unmarshal(data, &p) != nil {
		return nil, false
	}
	return &p, true
}

func pomModules(p *pom) []string {
	mods := append([]string{}, p.Modules...)
	for _, pr := range p.Profiles {
		mods = append(mods, pr.Modules...)
	}
	return mods
}

// maven follows <modules>, the parent POM (<relativePath>, default
// ../pom.xml), and dependencies on other modules of the same reactor.
func (r *resolver) maven(ctx context.Context, f string, data []byte) ([]cand, error) {
	p, ok := parsePOM(data)
	if !ok {
		return nil, nil
	}
	dir := dirOf(f)
	var out []cand
	for _, m := range pomModules(p) {
		m = strings.TrimSpace(m)
		t := join(dir, m)
		if strings.HasSuffix(m, ".xml") {
			t = dirOf(t)
		}
		out = append(out, cand{t, f, fmt.Sprintf("<module>%s</module>", m)})
	}
	if p.Parent != nil {
		rel, spec := "../pom.xml", "<parent> (default ../pom.xml)"
		if p.Parent.RelativePath != nil {
			rel = strings.TrimSpace(*p.Parent.RelativePath)
			spec = fmt.Sprintf("<relativePath>%s</relativePath>", rel)
		}
		if rel != "" {
			t := join(dir, rel)
			if t != "" && !strings.HasSuffix(t, ".xml") {
				t = path.Join(t, "pom.xml")
			}
			out = append(out, cand{t, f, spec})
		}
	}
	if len(p.Dependencies) > 0 {
		reactor, err := r.mavenReactor(ctx, dir)
		if err != nil {
			return nil, err
		}
		for _, d := range p.Dependencies {
			if m, ok := reactor[strings.TrimSpace(d.ArtifactID)]; ok && m != dir {
				out = append(out, cand{m, f, fmt.Sprintf("<artifactId>%s</artifactId>", d.ArtifactID)})
			}
		}
	}
	return out, nil
}

// mavenReactor maps artifactIds to module directories for the topmost POM
// at or above dir and every module under it.
func (r *resolver) mavenReactor(ctx context.Context, dir string) (map[string]string, error) {
	top := ""
	found := false
	for _, a := range ancestors(dir) {
		if k, err := r.src.Stat(ctx, path.Join(a, "pom.xml")); err != nil {
			return nil, err
		} else if k == File {
			top, found = a, true
		}
	}
	if !found {
		return nil, nil
	}
	key := "maven:" + top
	if v, ok := r.roots[key]; ok {
		return v.(map[string]string), nil
	}
	ids := map[string]string{}
	level := []string{top}
	seen := map[string]bool{}
	for depth := 0; len(level) > 0 && depth < 8; depth++ {
		if err := r.src.Prefetch(ctx, level); err != nil {
			return nil, err
		}
		var manifests []string
		for _, d := range level {
			manifests = append(manifests, path.Join(d, "pom.xml"))
		}
		if err := r.src.Prefetch(ctx, r.existing(ctx, manifests)); err != nil {
			return nil, err
		}
		var next []string
		for _, d := range level {
			if seen[d] {
				continue
			}
			seen[d] = true
			data, err := r.read(ctx, path.Join(d, "pom.xml"))
			if err != nil {
				return nil, err
			}
			p, ok := parsePOM(data)
			if !ok {
				continue
			}
			if id := strings.TrimSpace(p.ArtifactID); id != "" {
				ids[id] = d
			}
			for _, m := range pomModules(p) {
				t := join(d, strings.TrimSpace(m))
				if strings.HasSuffix(m, ".xml") {
					t = dirOf(t)
				}
				if t != "" {
					next = append(next, t)
				}
			}
		}
		level = next
	}
	r.roots[key] = ids
	return ids, nil
}

// --- Terraform, Helm, TypeScript ---

var tfSource = regexp.MustCompile(`(?m)(?:^|[\s{;])source\s*=\s*"(\.\.?/[^"]*)"`)

// terraform follows local module sources.
func terraform(f string, data []byte) []cand {
	dir := dirOf(f)
	var out []cand
	for _, m := range tfSource.FindAllSubmatch(data, -1) {
		rel := string(m[1])
		out = append(out, cand{join(dir, rel), f, fmt.Sprintf("source = %q", rel)})
	}
	return out
}

var helmRepo = regexp.MustCompile(`repository:\s*["']?file://([^"'\s]+)`)

// helm follows file:// chart dependencies.
func helm(f string, data []byte) []cand {
	dir := dirOf(f)
	var out []cand
	for _, m := range helmRepo.FindAllSubmatch(data, -1) {
		rel := string(m[1])
		out = append(out, cand{join(dir, rel), f, "repository: file://" + rel})
	}
	return out
}

var (
	tsExtends    = regexp.MustCompile(`"extends"\s*:\s*("(?:[^"\\]|\\.)*"|\[[^\]]*\])`)
	tsReferences = regexp.MustCompile(`"references"\s*:\s*\[([^\]]*)\]`)
	tsPath       = regexp.MustCompile(`"path"\s*:\s*"([^"]+)"`)
	jsonString   = regexp.MustCompile(`"((?:[^"\\]|\\.)*)"`)
)

// tsconfig follows relative "extends" (the file) and project "references"
// (the referenced project's directory).
func (r *resolver) tsconfig(ctx context.Context, f string, data []byte) ([]cand, error) {
	dir := dirOf(f)
	src := stripJSONComments(data)
	var out []cand
	for _, m := range tsExtends.FindAllSubmatch(src, -1) {
		for _, s := range jsonString.FindAllSubmatch(m[1], -1) {
			rel := string(s[1])
			if !strings.HasPrefix(rel, ".") {
				continue // a package; npm brings it
			}
			t := join(dir, rel)
			if t == "" {
				continue
			}
			k, err := r.src.Stat(ctx, t)
			if err != nil {
				return nil, err
			}
			switch {
			case k == Dir:
				t = path.Join(t, "tsconfig.json")
			case k == Missing && !strings.HasSuffix(t, ".json"):
				t += ".json"
			}
			out = append(out, cand{t, f, fmt.Sprintf(`"extends": %q`, rel)})
		}
	}
	for _, m := range tsReferences.FindAllSubmatch(src, -1) {
		for _, p := range tsPath.FindAllSubmatch(m[1], -1) {
			rel := string(p[1])
			t := join(dir, rel)
			if t == "" {
				continue
			}
			if k, err := r.src.Stat(ctx, t); err != nil {
				return nil, err
			} else if k == File {
				t = dirOf(t)
			}
			out = append(out, cand{t, f, fmt.Sprintf(`"references": [{"path": %q}]`, rel)})
		}
	}
	return out, nil
}

// stripJSONComments blanks // and /* */ comments outside strings (tsconfig
// files are JSON with comments).
func stripJSONComments(data []byte) []byte {
	out := make([]byte, 0, len(data))
	inStr := false
	for i := 0; i < len(data); i++ {
		c := data[i]
		switch {
		case inStr:
			out = append(out, c)
			if c == '\\' && i+1 < len(data) {
				i++
				out = append(out, data[i])
			} else if c == '"' {
				inStr = false
			}
		case c == '"':
			inStr = true
			out = append(out, c)
		case c == '/' && i+1 < len(data) && data[i+1] == '/':
			for i < len(data) && data[i] != '\n' {
				i++
			}
			out = append(out, '\n')
		case c == '/' && i+1 < len(data) && data[i+1] == '*':
			i += 2
			for i+1 < len(data) && !(data[i] == '*' && data[i+1] == '/') {
				i++
			}
			i++
		default:
			out = append(out, c)
		}
	}
	return out
}

// --- small readers for TOML and YAML subsets ---

type tomlKV struct{ key, value string }

type tomlSection struct {
	name string // "" for top-level keys; [[x]] arrays keep their name
	kvs  []tomlKV
}

// tomlSections splits a TOML file into tables of key = value pairs, with
// comments removed and multi-line arrays and inline tables joined. Quoted
// keys and table names are unquoted. That's all manifests need here.
func tomlSections(data []byte) []tomlSection {
	secs := []tomlSection{{}}
	lines := strings.Split(string(data), "\n")
	for i := 0; i < len(lines); i++ {
		line := strings.TrimSpace(stripTOMLComment(lines[i]))
		if line == "" {
			continue
		}
		if strings.HasPrefix(line, "[") {
			name := strings.Trim(line, "[] ")
			secs = append(secs, tomlSection{name: unquoteKey(name)})
			continue
		}
		key, value, ok := strings.Cut(line, "=")
		if !ok {
			continue
		}
		value = strings.TrimSpace(value)
		for depth := bracketDepth(value); depth > 0 && i+1 < len(lines); depth = bracketDepth(value) {
			i++
			value += " " + strings.TrimSpace(stripTOMLComment(lines[i]))
		}
		secs[len(secs)-1].kvs = append(secs[len(secs)-1].kvs, tomlKV{unquoteKey(strings.TrimSpace(key)), value})
	}
	return secs
}

// unquoteKey turns `target.'cfg(unix)'.dependencies` or `"a".b` into a
// plain dotted name.
func unquoteKey(k string) string {
	var parts []string
	for _, p := range splitOutsideQuotes(k, '.') {
		parts = append(parts, strings.Trim(strings.TrimSpace(p), `"'`))
	}
	return strings.Join(parts, ".")
}

func splitOutsideQuotes(s string, sep byte) []string {
	var out []string
	var q byte
	start := 0
	for i := 0; i < len(s); i++ {
		switch c := s[i]; {
		case q != 0:
			if c == q {
				q = 0
			}
		case c == '"' || c == '\'':
			q = c
		case c == sep:
			out = append(out, s[start:i])
			start = i + 1
		}
	}
	return append(out, s[start:])
}

func stripTOMLComment(line string) string {
	var q byte
	for i := 0; i < len(line); i++ {
		switch c := line[i]; {
		case q != 0:
			if c == '\\' && q == '"' {
				i++
			} else if c == q {
				q = 0
			}
		case c == '"' || c == '\'':
			q = c
		case c == '#':
			return line[:i]
		}
	}
	return line
}

// bracketDepth counts unclosed [ and { outside strings.
func bracketDepth(s string) int {
	depth := 0
	var q byte
	for i := 0; i < len(s); i++ {
		switch c := s[i]; {
		case q != 0:
			if c == q {
				q = 0
			}
		case c == '"' || c == '\'':
			q = c
		case c == '[' || c == '{':
			depth++
		case c == ']' || c == '}':
			depth--
		}
	}
	return depth
}

var tomlString = regexp.MustCompile(`"((?:[^"\\]|\\.)*)"|'([^']*)'`)

// tomlStrings returns every string literal in a value.
func tomlStrings(v string) []string {
	var out []string
	for _, m := range tomlString.FindAllStringSubmatch(v, -1) {
		out = append(out, m[1]+m[2])
	}
	return out
}

func firstString(v string) string {
	if s := tomlStrings(v); len(s) > 0 {
		return s[0]
	}
	return ""
}

// tomlField reads field from `name = { field = "x" }` or `name.field = "x"`.
func tomlField(kv tomlKV, field string) string {
	if _, f, ok := strings.Cut(kv.key, "."); ok {
		if f == field {
			return firstString(kv.value)
		}
		return ""
	}
	if !strings.HasPrefix(kv.value, "{") {
		return ""
	}
	re := regexp.MustCompile(`(?:^|[{,\s])` + regexp.QuoteMeta(field) + `\s*=\s*("(?:[^"\\]|\\.)*"|'[^']*')`)
	if m := re.FindStringSubmatch(kv.value); m != nil {
		return firstString(m[1])
	}
	return ""
}

var inlineWorkspace = regexp.MustCompile(`(?:^|[{,\s])workspace\s*=\s*true`)

func inlineBool(v, field string) bool {
	return field == "workspace" && strings.HasPrefix(v, "{") && inlineWorkspace.MatchString(v)
}

// yamlList returns the items of a top-level block list (key:\n  - a\n  - b).
func yamlList(data []byte, key string) []string {
	var out []string
	in := false
	for _, line := range strings.Split(string(data), "\n") {
		if i := strings.Index(line, " #"); i >= 0 {
			line = line[:i]
		}
		trimmed := strings.TrimSpace(line)
		switch {
		case strings.HasPrefix(line, key+":"):
			in = true
		case in && strings.HasPrefix(trimmed, "- "):
			out = append(out, strings.Trim(strings.TrimSpace(trimmed[2:]), `"'`))
		case in && trimmed != "" && !strings.HasPrefix(line, " ") && !strings.HasPrefix(line, "\t"):
			in = false
		}
	}
	return out
}

func dirOf(f string) string {
	if d := path.Dir(f); d != "." {
		return d
	}
	return ""
}

func cutAny(s string, prefixes ...string) (string, bool) {
	for _, p := range prefixes {
		if rest, ok := strings.CutPrefix(s, p); ok {
			return rest, true
		}
	}
	return "", false
}

func sortedKeys(m map[string]string) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}
