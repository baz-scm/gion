package deps

import (
	"context"
	"fmt"
	"path"
	"sort"
	"strings"
)

// ConfigFile is a repo's own gion config:
//
//	[projects]                 # names for starting paths
//	bff = ["apps/bff"]
//	web = ["apps/web", "packages/ui"]
//
//	[deps]                     # dependencies manifests don't show
//	"apps/web" = ["proto/web", "tools/codegen"]
//
// A project lists only where to start; local dependencies are still
// inferred, so it doesn't go stale as manifests change. A [deps] rule
// applies whenever its path joins a workspace, however it got there.
const ConfigFile = ".gion/projects.toml"

// Config is a parsed ConfigFile.
type Config struct {
	File     string // where it was read from, for messages
	Projects map[string][]string
	Deps     map[string][]string // path -> paths it needs
}

// ParseConfig reads a ConfigFile. Unknown tables are ignored, so later
// versions can add some.
func ParseConfig(data []byte) (Config, error) { return parseConfig(ConfigFile, data) }

func parseConfig(file string, data []byte) (Config, error) {
	c := Config{File: file, Projects: map[string][]string{}, Deps: map[string][]string{}}
	for _, s := range tomlSections(data) {
		var into map[string][]string
		switch s.name {
		case "projects":
			into = c.Projects
		case "deps":
			into = c.Deps
		default:
			continue
		}
		for _, kv := range s.kvs {
			key := kv.key
			if s.name == "deps" {
				p, err := configPath(key)
				if err != nil {
					return c, fmt.Errorf("%s: [deps]: %w", file, err)
				}
				key = p
			} else if key == "" || strings.ContainsAny(key, " \t/") {
				return c, fmt.Errorf("%s: bad project name %q", file, kv.key)
			}
			if _, dup := into[key]; dup {
				return c, fmt.Errorf("%s: [%s] %q is set twice", file, s.name, key)
			}
			if !strings.HasPrefix(kv.value, "[") {
				return c, fmt.Errorf("%s: [%s] %q: want a list of paths, like [\"a/b\"]", file, s.name, key)
			}
			paths := tomlStrings(kv.value)
			if len(paths) == 0 {
				return c, fmt.Errorf("%s: [%s] %q has no paths", file, s.name, key)
			}
			for i, p := range paths {
				clean, err := configPath(p)
				if err != nil {
					return c, fmt.Errorf("%s: [%s] %q: %w", file, s.name, key, err)
				}
				paths[i] = clean
			}
			into[key] = paths
		}
	}
	return c, nil
}

// configPath cleans a repo path from the config. The repo root and gion's
// own directory can't be named.
func configPath(p string) (string, error) {
	if strings.HasPrefix(p, "../") || p == ".." {
		return "", fmt.Errorf("path %q is outside the repo", p)
	}
	clean := strings.Trim(path.Clean("/"+p), "/")
	if clean == "" || clean == ".gion" || strings.HasPrefix(clean, ".gion/") {
		return "", fmt.Errorf("bad path %q", p)
	}
	return clean, nil
}

// LoadConfig reads src's ConfigFile; a repo without one has an empty config.
func LoadConfig(ctx context.Context, src Source) (Config, error) {
	file := ConfigFile
	if cs, ok := src.(configSource); ok {
		file = cs.name
	}
	k, err := src.Stat(ctx, ConfigFile)
	if err != nil || k != File {
		return Config{File: file}, err
	}
	data, err := src.ReadFile(ctx, ConfigFile)
	if err != nil {
		return Config{File: file}, err
	}
	return parseConfig(file, data)
}

// ProjectPaths returns the starting paths of the named projects.
func (c Config) ProjectPaths(names []string) ([]string, error) {
	if len(c.Projects) == 0 {
		return nil, fmt.Errorf("no projects: %s has no [projects]; use --path", c.file())
	}
	var out []string
	for _, n := range names {
		ps, ok := c.Projects[n]
		if !ok {
			known := make([]string, 0, len(c.Projects))
			for k := range c.Projects {
				known = append(known, k)
			}
			sort.Strings(known)
			return nil, fmt.Errorf("no project %q in %s (defined: %s)", n, c.file(), strings.Join(known, ", "))
		}
		out = append(out, ps...)
	}
	return out, nil
}

// rules returns the [deps] candidates of paths: each rule whose path is
// one of them or under one.
func (c Config) rules(paths []string) []cand {
	keys := make([]string, 0, len(c.Deps))
	for k := range c.Deps {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	var out []cand
	for _, k := range keys {
		if !covered(paths, k) {
			continue
		}
		for _, p := range c.Deps[k] {
			out = append(out, cand{path: p, manifest: c.file(), spec: k})
		}
	}
	return out
}

// WithConfig reads src, but with data, read from file, as its ConfigFile:
// a workspace's copy, edited but not yet committed.
func WithConfig(src Source, file string, data []byte) Source {
	return configSource{src, file, data}
}

type configSource struct {
	Source
	name string
	data []byte
}

func (s configSource) Stat(ctx context.Context, p string) (Kind, error) {
	if p == ConfigFile {
		return File, nil
	}
	return s.Source.Stat(ctx, p)
}

func (s configSource) ReadFile(ctx context.Context, p string) ([]byte, error) {
	if p == ConfigFile {
		return s.data, nil
	}
	return s.Source.ReadFile(ctx, p)
}

func (c Config) file() string {
	if c.File == "" {
		return ConfigFile
	}
	return c.File
}
