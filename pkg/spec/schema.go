package spec

import (
	"bytes"
	_ "embed"
	"fmt"
	"sync"

	"github.com/santhosh-tekuri/jsonschema/v6"
)

//go:embed schema/vm-config-v1.schema.json
var schemaJSON []byte

// SchemaID is the $id of the embedded config schema.
const SchemaID = "https://github.com/weaveplatform/weaveplatform-oci/pkg/spec/schema/vm-config-v1.schema.json"

// Schema returns a copy of the embedded JSON Schema for the config document.
func Schema() []byte {
	return bytes.Clone(schemaJSON)
}

var compiled = sync.OnceValues(func() (*jsonschema.Schema, error) {
	doc, err := jsonschema.UnmarshalJSON(bytes.NewReader(schemaJSON))
	if err != nil {
		return nil, fmt.Errorf("embedded schema: %w", err)
	}
	c := jsonschema.NewCompiler()
	c.AssertFormat()
	c.AssertContent()
	if err := c.AddResource(SchemaID, doc); err != nil {
		return nil, fmt.Errorf("embedded schema: %w", err)
	}
	s, err := c.Compile(SchemaID)
	if err != nil {
		return nil, fmt.Errorf("embedded schema: %w", err)
	}
	return s, nil
})

func validateSchema(raw []byte) error {
	s, err := compiled()
	if err != nil {
		return err
	}
	doc, err := jsonschema.UnmarshalJSON(bytes.NewReader(raw))
	if err != nil {
		return fmt.Errorf("parse: %w", err)
	}
	if err := s.Validate(doc); err != nil {
		return fmt.Errorf("schema: %w", err)
	}
	return nil
}
