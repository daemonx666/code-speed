// Ported from magpie (internal/edit/yaml.go, yaml_top.go), MIT License,
// (c) 2026 yetone. Only SetYAML and DelYAML are kept: the line-based top-level
// editors, the read-only looks, Mark notes and the node-callback editors are not.

package edit

import (
	"bytes"
	"fmt"
	"io"
	"strings"

	"gopkg.in/yaml.v3"
)

// Nested YAML keys are edited through yaml.v3's node tree, which keeps
// comments and key order; indentation comes out as two spaces.

// SetYAML sets key paths in a YAML file; a value may be a scalar, a map,
// a slice, a struct with yaml tags or YAMLText. Missing files and parents
// are created. Only the first mapping document can be edited, with empty or
// null trailing documents allowed; invalid input or output leaves the file
// untouched.
func SetYAML(path string, kvs ...KV) error {
	root, err := loadYAMLForEdit(path)
	if err != nil {
		return err
	}
	if root == nil {
		root = &yaml.Node{Kind: yaml.MappingNode}
	}
	for _, kv := range kvs {
		var v yaml.Node
		if err := v.Encode(kv.Value); err != nil {
			return fmt.Errorf("%s: %w", path, err)
		}
		setYAML(root, strings.Split(kv.Path, "."), &v)
	}
	return writeYAML(path, root)
}

// DelYAML removes key paths from a YAML file. A missing file stays missing.
func DelYAML(path string, keyPaths ...string) error {
	root, err := loadYAMLForEdit(path)
	if err != nil || root == nil {
		return err
	}
	changed := false
	for _, kp := range keyPaths {
		parts := strings.Split(kp, ".")
		parent := root
		if len(parts) > 1 {
			parent = lookupYAML(root, parts[:len(parts)-1])
		}
		if parent == nil || parent.Kind != yaml.MappingNode {
			continue
		}
		key := parts[len(parts)-1]
		for i := 0; i+1 < len(parent.Content); i += 2 {
			if parent.Content[i].Value == key {
				parent.Content = append(parent.Content[:i], parent.Content[i+2:]...)
				changed = true
				break
			}
		}
	}
	if !changed {
		return nil
	}
	return writeYAML(path, root)
}

// loadYAMLForEdit validates input even when the requested key is unchanged
// or missing.
func loadYAMLForEdit(path string) (*yaml.Node, error) {
	raw, err := Read(path)
	if err != nil {
		return nil, err
	}
	root, _, err := parseYAMLTop(raw)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	return root, nil
}

// parseYAMLDocument validates the first document and any empty/null trailing ones.
func parseYAMLDocument(raw []byte) (*yaml.Node, error) {
	root, _, err := parseYAMLDocuments(raw)
	return root, err
}

// Decode as well as parse: yaml.Node alone accepts duplicate mapping keys.
// Only empty or null trailing documents can be discarded safely. nextLine
// bounds the first document for line edits; it is zero when no other follows.
func parseYAMLDocuments(raw []byte) (root *yaml.Node, nextLine int, err error) {
	dec := yaml.NewDecoder(bytes.NewReader(raw))
	var doc yaml.Node
	if err := dec.Decode(&doc); err == io.EOF {
		return nil, 0, nil
	} else if err != nil {
		return nil, 0, err
	}
	for {
		var extra yaml.Node
		if err := dec.Decode(&extra); err == io.EOF {
			break
		} else if err != nil {
			return nil, 0, err
		}
		if nextLine == 0 {
			nextLine = extra.Line
		}
		if len(extra.Content) == 0 {
			continue
		}
		n := extra.Content[0]
		if n.Kind != yaml.ScalarNode || n.ShortTag() != "!!null" {
			return nil, 0, fmt.Errorf("expected a single YAML document")
		}
		var null any
		if err := n.Decode(&null); err != nil {
			return nil, 0, err
		}
	}
	if len(doc.Content) == 0 {
		return nil, nextLine, nil
	}
	root = doc.Content[0]
	var decoded any
	if err := root.Decode(&decoded); err != nil {
		return nil, 0, err
	}
	// comments above the first key belong to the document
	if doc.HeadComment != "" && root.HeadComment == "" {
		root.HeadComment = doc.HeadComment
	}
	return root, nextLine, nil
}

func writeYAML(path string, root *yaml.Node) error {
	var buf bytes.Buffer
	enc := yaml.NewEncoder(&buf)
	enc.SetIndent(2)
	if err := enc.Encode(root); err != nil {
		return fmt.Errorf("%s: %w", path, err)
	}
	if err := enc.Close(); err != nil {
		return fmt.Errorf("%s: %w", path, err)
	}
	// Reparse the rendered text: replacing or removing a node can leave a
	// dangling alias even though the original node tree was valid.
	if _, err := parseYAMLDocument(buf.Bytes()); err != nil {
		return fmt.Errorf("%s: %w", path, err)
	}
	orig, _ := Read(path)
	return WriteAtomic(path, keepCRLF(buf.Bytes(), orig))
}

func lookupYAML(n *yaml.Node, parts []string) *yaml.Node {
	for _, p := range parts {
		if n.Kind != yaml.MappingNode {
			return nil
		}
		var next *yaml.Node
		for i := 0; i+1 < len(n.Content); i += 2 {
			if n.Content[i].Value == p {
				next = n.Content[i+1]
				break
			}
		}
		if next == nil {
			return nil
		}
		n = next
	}
	return n
}

// setYAML puts v at parts under the mapping m. An existing value is
// replaced in place, keeping the comments beside it; a scalar in the way
// of a deeper key becomes a mapping.
func setYAML(m *yaml.Node, parts []string, v *yaml.Node) {
	for i, p := range parts {
		last := i == len(parts)-1
		var cur *yaml.Node
		for j := 0; j+1 < len(m.Content); j += 2 {
			if m.Content[j].Value == p {
				cur = m.Content[j+1]
				break
			}
		}
		if cur == nil {
			// the {} a key taken out left (Hermes's providers: {} after
			// a disconnect) is filled in block style, not on one line
			if len(m.Content) == 0 {
				m.Style &^= yaml.FlowStyle
			}
			cur = &yaml.Node{Kind: yaml.MappingNode}
			if last {
				cur = v
			}
			m.Content = append(m.Content, &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: p}, cur)
			m = cur
			continue
		}
		if last {
			v.HeadComment, v.LineComment, v.FootComment = cur.HeadComment, cur.LineComment, cur.FootComment
			*cur = *v
			return
		}
		if cur.Kind != yaml.MappingNode {
			*cur = yaml.Node{Kind: yaml.MappingNode, HeadComment: cur.HeadComment, LineComment: cur.LineComment}
		}
		m = cur
	}
}

// parseYAMLTop requires a mapping root, treating an empty null document as empty.
// nextLine is the first trailing document's line, or zero when none follows.
func parseYAMLTop(raw []byte) (root *yaml.Node, nextLine int, err error) {
	root, nextLine, err = parseYAMLDocuments(raw)
	if err != nil || root == nil {
		return nil, nextLine, err
	}
	if root.Kind == yaml.ScalarNode && root.Tag == "!!null" && root.Value == "" {
		return nil, nextLine, nil
	}
	if root.Kind != yaml.MappingNode {
		return nil, 0, fmt.Errorf("line %d: top level is not a YAML mapping", root.Line)
	}
	return root, nextLine, nil
}
