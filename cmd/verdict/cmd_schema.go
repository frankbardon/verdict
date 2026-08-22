package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"

	jsonschema "github.com/santhosh-tekuri/jsonschema/v6"
	"github.com/urfave/cli/v3"

	"github.com/frankbardon/verdict/pkg/dmn/vdj"
)

func cmdSchema() *cli.Command {
	return &cli.Command{
		Name:  "schema",
		Usage: "print the JSON Schema for Verdict Decision JSON",
		Description: "Writes the JSON Schema (draft 2020-12) describing a VDJ document to stdout.\n" +
			"It needs no model and no network: the schema is generated from the same\n" +
			"types the reader decodes into and the same vocabulary the engine evaluates.\n\n" +
			"The output is byte-identical to the copy published at\n" +
			"  " + vdj.SchemaID + "\n" +
			"which is also the schema's own $id, so a validator that resolves the\n" +
			"identifier and a validator handed this file agree.\n\n" +
			"Point an editor at it to get completion and validation while writing a\n" +
			"model by hand:\n\n" +
			"  verdict schema > vdj-schema.json\n\n" +
			"Or check a file directly, which needs no other tooling:\n\n" +
			"  verdict schema --validate model.vdj\n\n" +
			"That is worth doing before `verdict eval`: the loader is deliberately\n" +
			"lenient about fields it does not recognise, so a mistyped key loads as a\n" +
			"silent omission rather than an error. The schema is where it is caught.",
		Flags: []cli.Flag{
			&cli.StringFlag{
				Name:    "out",
				Aliases: []string{"o"},
				Usage:   "write to this file instead of stdout",
			},
			&cli.StringFlag{
				Name:  "validate",
				Usage: "validate a VDJ document against the schema instead of printing it",
			},
		},
		Action: func(_ context.Context, cmd *cli.Command) error {
			if path := cmd.String("validate"); path != "" {
				return validateDocument(path)
			}
			out := append(vdj.BuildSchema(), '\n')
			if dest := cmd.String("out"); dest != "" {
				if err := os.WriteFile(dest, out, 0o644); err != nil {
					return fmt.Errorf("writing %s: %w", dest, err)
				}
				return nil
			}
			_, err := os.Stdout.Write(out)
			return err
		},
	}
}

// validateDocument checks one VDJ file against the published schema.
//
// It exists because the alternative is asking every user to install a
// JSON Schema validator before they can use the contract Verdict publishes —
// and because the loader is deliberately lenient about unknown fields, so a
// mistyped key is silently ignored at load and caught only here.
func validateDocument(path string) error {
	raw, err := os.ReadFile(path)
	if err != nil {
		return fmt.Errorf("reading %s: %w", path, err)
	}
	var doc any
	if err := json.Unmarshal(raw, &doc); err != nil {
		return fmt.Errorf("%s is not valid JSON: %w", path, err)
	}

	var schemaDoc any
	if err := json.Unmarshal(vdj.BuildSchema(), &schemaDoc); err != nil {
		return fmt.Errorf("decoding the schema: %w", err)
	}
	c := jsonschema.NewCompiler()
	if err := c.AddResource(vdj.SchemaID, schemaDoc); err != nil {
		return fmt.Errorf("loading the schema: %w", err)
	}
	schema, err := c.Compile(vdj.SchemaID)
	if err != nil {
		return fmt.Errorf("compiling the schema: %w", err)
	}

	if err := schema.Validate(doc); err != nil {
		// The validator's detailed output names the failing keyword and the
		// path to it, which is what a person needs to fix the file. Exit 2:
		// the command worked, the document did not.
		fmt.Fprintf(os.Stderr, "%s does not validate against %s:\n%v\n", path, vdj.SchemaID, err)
		return exitWithFindings()
	}
	fmt.Printf("%s validates against %s\n", path, vdj.SchemaID)
	return nil
}
