// Package strictyaml decodes the YAML a person writes by hand, refusing keys
// the struct does not have.
//
// The permissive default is the wrong trade for these files. An engagement's
// evalset was written with `expect:` where the field is `expect_metrics:`; every
// case decoded to an empty expectation, every case failed, and the acceptance
// report went out saying the delivery answered 0% of the customer's questions
// correctly. Nothing errored. The number was simply wrong, in the one document
// whose entire job is to say whether the numbers are right.
//
// A typo in a config file is not a rare event, and "silently ignored" is the
// worst of the three possible responses to one.
package strictyaml

import (
	"bytes"
	"fmt"

	"gopkg.in/yaml.v3"
)

// Unmarshal decodes b into v, erroring on any field not present in v.
//
// The error names the file so the message is actionable from a CI log, where
// the reader has no idea which of four YAML files was being read.
func Unmarshal(name string, b []byte, v any) error {
	dec := yaml.NewDecoder(bytes.NewReader(b))
	dec.KnownFields(true)
	if err := dec.Decode(v); err != nil {
		return fmt.Errorf("%s: %w", name, err)
	}
	return nil
}
