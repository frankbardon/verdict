package main

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/urfave/cli/v3"

	"github.com/frankbardon/verdict/pkg/dmn/vdj"
	dmnxml "github.com/frankbardon/verdict/pkg/dmn/xml"
)

func cmdConvert() *cli.Command {
	return &cli.Command{
		Name:      "convert",
		Usage:     "convert a model between DMN XML and Verdict Decision JSON",
		ArgsUsage: "<model.dmn|model.vdj>",
		Description: "Reads a model in either format and writes it in the other, or in the format\n" +
			"named by --to. The projection is lossless: converting in both directions\n" +
			"reproduces the model the evaluator sees.\n\n" +
			"DMN XML is written at version 1.3 by default, because that is what Camunda\n" +
			"Modeler and dmn-js read. Pass --dmn-version 1.5 for the newest namespace\n" +
			"Verdict supports, at the cost of most editors refusing to open it.\n\n" +
			"Diagram interchange is generated automatically: a DMN document without it\n" +
			"is schema-valid but opens as an empty canvas.",
		Flags: []cli.Flag{
			&cli.StringFlag{
				Name:  "to",
				Usage: "output format: xml or json (default: the opposite of the input)",
			},
			&cli.StringFlag{
				Name:  "dmn-version",
				Value: "1.3",
				Usage: "DMN namespace to emit: 1.3, 1.4 or 1.5",
			},
			&cli.BoolFlag{
				Name:  "no-diagram",
				Usage: "omit diagram interchange; the model will open as an empty canvas",
			},
			&cli.StringFlag{
				Name:    "out",
				Aliases: []string{"o"},
				Usage:   "write to this file instead of stdout",
			},
		},
		Action: func(ctx context.Context, cmd *cli.Command) error {
			path := cmd.Args().First()
			if path == "" {
				return fmt.Errorf("convert needs a model file")
			}
			_, m, err := openModel(cmd, path)
			if err != nil {
				return err
			}

			format := cmd.String("to")
			if format == "" {
				switch strings.ToLower(filepath.Ext(path)) {
				case ".json", ".vdj":
					format = "xml"
				default:
					format = "json"
				}
			}

			var out []byte
			switch format {
			case "xml", "dmn":
				ns, nsErr := dmnNamespace(cmd.String("dmn-version"))
				if nsErr != nil {
					return nsErr
				}
				out, err = dmnxml.MarshalWith(m.Definitions(), dmnxml.Options{
					Namespace: ns,
					Diagram:   !cmd.Bool("no-diagram"),
				})
			case "json", "vdj":
				out, err = vdj.Marshal(m.Definitions())
			default:
				return fmt.Errorf("unknown output format %q (want xml or json)", format)
			}
			if err != nil {
				return err
			}

			if dest := cmd.String("out"); dest != "" {
				return os.WriteFile(dest, append(out, '\n'), 0o644)
			}
			_, err = os.Stdout.Write(append(out, '\n'))
			return err
		},
	}
}

// dmnNamespace maps a version flag onto a DMN MODEL namespace.
func dmnNamespace(version string) (string, error) {
	switch strings.TrimSpace(version) {
	case "", "1.3":
		return dmnxml.NS13, nil
	case "1.4":
		return dmnxml.NS14, nil
	case "1.5":
		return dmnxml.NS15, nil
	default:
		return "", fmt.Errorf("unknown DMN version %q (want 1.3, 1.4 or 1.5)", version)
	}
}
