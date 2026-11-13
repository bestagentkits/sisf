package detect

import (
	"fmt"
	"os"
	"sort"
	"strconv"
	"strings"

	"gopkg.in/yaml.v3"
)

// composeFile is the part of a Compose file adoption reads. Everything else
// in the file is ignored: adoption describes the application, it does not
// copy how the Owner happened to run it.
type composeFile struct {
	Services map[string]composeService `yaml:"services"`
	Volumes  map[string]any            `yaml:"volumes"`
	Secrets  map[string]struct {
		File string `yaml:"file"`
	} `yaml:"secrets"`
}

type composeService struct {
	Image       string   `yaml:"image"`
	Build       any      `yaml:"build"`
	User        string   `yaml:"user"`
	Ports       []any    `yaml:"ports"`
	Environment envBlock `yaml:"environment"`
	Volumes     []any    `yaml:"volumes"`
	Secrets     []any    `yaml:"secrets"`
	Command     any      `yaml:"command"`
}

// envBlock accepts both forms Compose allows: a map and a list of K=V.
type envBlock map[string]string

func (e *envBlock) UnmarshalYAML(n *yaml.Node) error {
	out := envBlock{}
	switch n.Kind {
	case yaml.MappingNode:
		var m map[string]any
		if err := n.Decode(&m); err != nil {
			return err
		}
		for k, v := range m {
			out[k] = fmt.Sprint(v)
		}
	case yaml.SequenceNode:
		var l []string
		if err := n.Decode(&l); err != nil {
			return err
		}
		for _, kv := range l {
			k, v, _ := strings.Cut(kv, "=")
			out[k] = v
		}
	default:
		return fmt.Errorf("environment is neither a map nor a list")
	}
	*e = out
	return nil
}

func readCompose(path string) (*composeFile, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var f composeFile
	if err := yaml.Unmarshal(data, &f); err != nil {
		return nil, fmt.Errorf("%s cannot be read as a Compose file: %w", path, err)
	}
	if len(f.Services) == 0 {
		return nil, fmt.Errorf("%s declares no services", path)
	}
	return &f, nil
}

// serviceNames returns the service names in a fixed order.
func (f *composeFile) serviceNames() []string {
	names := make([]string, 0, len(f.Services))
	for n := range f.Services {
		names = append(names, n)
	}
	sort.Strings(names)
	return names
}

// containerPort returns the container side of the service's first published
// port, or 0 when it publishes none.
func (s composeService) containerPort() int {
	for _, p := range s.Ports {
		var spec string
		switch v := p.(type) {
		case string:
			spec = v
		case int:
			spec = strconv.Itoa(v)
		case map[string]any:
			if t, ok := v["target"]; ok {
				spec = fmt.Sprint(t)
			}
		}
		spec, _, _ = strings.Cut(spec, "/")
		parts := strings.Split(spec, ":")
		if n, err := strconv.Atoi(parts[len(parts)-1]); err == nil {
			return n
		}
	}
	return 0
}

// namedVolumes returns the service's mounts of named volumes, target by
// name; bind mounts are left out.
func (s composeService) namedVolumes(declared map[string]any) map[string]string {
	out := map[string]string{}
	for _, v := range s.Volumes {
		str, ok := v.(string)
		if !ok {
			continue
		}
		parts := strings.Split(str, ":")
		if len(parts) < 2 {
			continue
		}
		if _, ok := declared[parts[0]]; ok {
			out[parts[0]] = parts[1]
		}
	}
	return out
}

// imageName returns the repository of an image reference without registry,
// tag or digest: "docker.io/library/wordpress:7.0@sha256:.." is "wordpress".
func imageName(ref string) string {
	ref, _, _ = strings.Cut(ref, "@")
	if i := strings.LastIndex(ref, "/"); i >= 0 {
		ref = ref[i+1:]
	}
	ref, _, _ = strings.Cut(ref, ":")
	return ref
}
