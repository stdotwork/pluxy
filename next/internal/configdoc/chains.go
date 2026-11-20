package configdoc

import (
	"encoding/json"
	"fmt"

	"gopkg.in/yaml.v3"
)

// SetChain writes the fallback chain name at routing.chains in a JSON config: steps is its list
// of steps as JSON. A config.yaml cannot hold one, since it speaks today's core's names and
// today's core has no chains.
func (d *Doc) SetChain(name string, steps json.RawMessage) error {
	if d.YAML {
		return fmt.Errorf("%w: a config.yaml cannot hold fallback chains; they are kept in a JSON config", ErrInvalid)
	}
	// JSON is YAML: its nodes carry each value's type, which the JSON writer needs.
	var n yaml.Node
	if err := yaml.Unmarshal(steps, &n); err != nil || n.Kind != yaml.DocumentNode || len(n.Content) != 1 || n.Content[0].Kind != yaml.SequenceNode {
		return fmt.Errorf("%w: routing.chains.%s: the steps must be a list", ErrInvalid, name)
	}
	return d.Set(n.Content[0], "routing", "chains", name)
}

// RemoveChain removes the fallback chain name, and routing.chains when it was the last; it
// reports whether there was one.
func (d *Doc) RemoveChain(name string) bool {
	if d.Get("routing", "chains", name) == nil {
		return false
	}
	d.Delete("routing", "chains", name)
	if c := d.Get("routing", "chains"); c != nil && c.Kind == yaml.MappingNode && len(c.Content) == 0 {
		d.Delete("routing", "chains")
	}
	return true
}
