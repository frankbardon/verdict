package verdict

import (
	"bytes"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/frankbardon/verdict/pkg/diag"
	"github.com/frankbardon/verdict/pkg/dmn/model"
	"github.com/frankbardon/verdict/pkg/dmn/vdj"
	dmnxml "github.com/frankbardon/verdict/pkg/dmn/xml"
)

// ModelSource is where a model comes from. Both DMN XML and Verdict Decision
// JSON are accepted; the format is detected from the file extension when there
// is one and from the content otherwise.
type ModelSource interface {
	read() (*model.Definitions, []diag.Diagnostic, error)
}

// FromFile loads a model from a path. The extension selects the reader:
// `.json` and `.vdj` are Verdict Decision JSON, anything else is DMN XML.
func FromFile(path string) ModelSource { return fileSource{path: path} }

// FromBytes loads a model from memory, detecting the format from the content.
func FromBytes(b []byte) ModelSource { return bytesSource{data: b} }

// FromReader loads a model from a reader, detecting the format from the content.
func FromReader(r io.Reader) ModelSource { return readerSource{r: r} }

// FromXML loads a model from DMN XML, skipping format detection.
func FromXML(b []byte) ModelSource { return bytesSource{data: b, format: formatXML} }

// FromJSON loads a model from Verdict Decision JSON, skipping format detection.
func FromJSON(b []byte) ModelSource { return bytesSource{data: b, format: formatJSON} }

// FromDefinitions registers an already-built model. It is how a program that
// constructs a DRG in Go — a test, a generator — gets it into the engine.
func FromDefinitions(defs *model.Definitions) ModelSource { return defsSource{defs: defs} }

type format int

const (
	formatAuto format = iota
	formatXML
	formatJSON
)

type fileSource struct{ path string }

func (s fileSource) read() (*model.Definitions, []diag.Diagnostic, error) {
	data, err := os.ReadFile(s.path)
	if err != nil {
		return nil, nil, fmt.Errorf("verdict: reading %s: %w", s.path, err)
	}
	f := formatAuto
	switch strings.ToLower(filepath.Ext(s.path)) {
	case ".json", ".vdj":
		f = formatJSON
	case ".dmn", ".xml":
		f = formatXML
	}
	defs, ds, err := parse(data, f)
	if err != nil {
		return nil, nil, fmt.Errorf("verdict: loading %s: %w", s.path, err)
	}
	if defs.ID == "" {
		// A document with neither id nor name still needs a registry key; the
		// file's base name is the least surprising one.
		defs.ID = strings.TrimSuffix(filepath.Base(s.path), filepath.Ext(s.path))
	}
	return defs, ds, nil
}

type bytesSource struct {
	data   []byte
	format format
}

func (s bytesSource) read() (*model.Definitions, []diag.Diagnostic, error) {
	return parse(s.data, s.format)
}

type readerSource struct{ r io.Reader }

func (s readerSource) read() (*model.Definitions, []diag.Diagnostic, error) {
	data, err := io.ReadAll(s.r)
	if err != nil {
		return nil, nil, fmt.Errorf("verdict: reading model: %w", err)
	}
	return parse(data, formatAuto)
}

type defsSource struct{ defs *model.Definitions }

func (s defsSource) read() (*model.Definitions, []diag.Diagnostic, error) {
	if s.defs == nil {
		return nil, nil, fmt.Errorf("verdict: nil definitions")
	}
	if s.defs.ID == "" {
		s.defs.ID = s.defs.Name
	}
	return s.defs, nil, nil
}

func parse(data []byte, f format) (*model.Definitions, []diag.Diagnostic, error) {
	if f == formatAuto {
		f = detect(data)
	}
	if f == formatJSON {
		return vdj.Parse(data)
	}
	return dmnxml.Parse(data)
}

// detect sniffs the format from the first non-whitespace, non-comment byte.
func detect(data []byte) format {
	// Strip a UTF-8 byte-order mark before sniffing: exporters that write one
	// would otherwise defeat the first-character test.
	data = bytes.TrimPrefix(data, []byte{0xEF, 0xBB, 0xBF})
	trimmed := bytes.TrimLeft(data, " \t\r\n")
	if len(trimmed) > 0 && (trimmed[0] == '{' || trimmed[0] == '[') {
		return formatJSON
	}
	return formatXML
}
