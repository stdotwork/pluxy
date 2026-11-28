package configdoc

import (
	"encoding/json"
	"fmt"

	"gopkg.in/yaml.v3"
)

func (d *Doc) SetChain(name string, steps json.RawMessage) error {
	if d.YAML {
		return fmt.Errorf("%w: a config.yaml cannot hold fallback chains; they are kept in a JSON config", ErrInvalid)
	}
	var n yaml.Node
	if err := yaml.Unmarshal(steps, &n); err != nil || n.Kind != yaml.DocumentNode || len(n.Content) != 1 || n.Content[0].Kind != yaml.SequenceNode {
		return fmt.Errorf("%w: routing.chains.%s: the steps must be a list", ErrInvalid, name)
	}
	return d.Set(n.Content[0], "routing", "chains", name)
}

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
