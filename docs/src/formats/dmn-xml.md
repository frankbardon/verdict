# DMN XML and Interoperability

Verdict **reads DMN 1.3, 1.4 and 1.5**, and **writes 1.3 by default**.

Writing the newest version it can read would be defensible and wrong: Camunda
Modeler and every editor built on dmn-js read 1.3 only. A model no editor opens
is not interoperable whatever the version number in it says.

```bash
verdict convert model.vdj --to xml --out model.dmn      # DMN 1.3, with a diagram
verdict convert model.vdj --to xml --dmn-version 1.5    # newest namespace, few readers
verdict convert model.vdj --to xml --no-diagram         # schema-valid, empty canvas
```

The reader is namespace-tolerant: it matches on local element names, so a
document from a tool that declares DMN under an unexpected prefix, or mixes
1.3 and 1.4 namespaces, still loads.

## Diagram interchange is generated

A DMN document without DMNDI is perfectly schema-valid and opens as an **empty
canvas** in every dmn-js editor. Verdict therefore generates diagram
interchange from the DRG whenever it writes XML: shapes laid out in dependency
layers, edges for every requirement, translated into the positive quadrant so
the diagram opens on its own content.

Coordinates are generated, never hand-maintained. If you change an example
model's shape, `make diagrams` regenerates them.

## Three ways the XSD is stricter than the reader

These bit us once already, which is why `make validate` exists. Verdict's own
reader accepts all three; the schema — and therefore Camunda — does not.

**1. `tDefinitions` admits foreign attributes only when namespaced.** Verdict's
own metadata travels as `verdict:version` and `verdict:conformanceLevel`, never
bare. `version` and `conformanceLevel` are not DMN attributes, and a bare one
fails validation for the whole document.

```xml
<definitions xmlns="https://www.omg.org/spec/DMN/20191111/MODEL/"
             xmlns:verdict="https://github.com/frankbardon/verdict/schema/1.0"
             verdict:version="2.1.0"
             verdict:conformanceLevel="feel">
```

**2. `tDecision`'s logic slot admits only DMN's own `expression` substitution
group.** A foreign element there — an `agentDecision`, say — fails validation
for the entire file. So an agent decision travels in `<extensionElements>`
instead, positioned by `tDMNElement`'s sequence: immediately after
`<description>`, before `<question>`.

```xml
<decision id="risk_tier" name="Risk Tier">
  <description>Assessed from the applicant's notes.</description>
  <extensionElements>
    <verdict:agentDecision>…</verdict:agentDecision>
  </extensionElements>
  <question>How risky is this applicant?</question>
  <variable name="RiskTier" typeRef="string"/>
  <informationRequirement>…</informationRequirement>
</decision>
```

A standard DMN tool that round-trips this model sees a typed extension element
in a slot the schema explicitly reserves for extensions, and either preserves it
or warns — rather than refusing the document.

**3. `tItemDefinition` is an `xsd:choice`.** A definition is either a
constrained simple type *or* a structure — never both. Emitting `typeRef` and
`itemComponent` together validates in Verdict's reader and fails everywhere else.

## Element order is fixed

`description` comes first in every element, because it comes from
`tDMNElement`. In a decision the order is:

```
description → extensionElements → question → allowedAnswers → variable → requirements → logic
```

Getting this wrong is the most common hand-authoring mistake, and the error
message from a validator points at the *second* element, not the misplaced one.

## Verify it, do not assert it

"It parses in our reader" is not a claim about anyone else's tool. Verdict
validates against the real OMG schemas, vendored under
`pkg/dmn/xml/testdata/schema/`:

```bash
make validate
```

```
OK   examples/content_moderation/content_moderation.dmn
OK   examples/loan_approval/loan_approval.dmn
OK   examples/pricing/pricing.dmn
```

The Go test suite runs the same validation (`pkg/dmn/xml/schema_test.go`) but
**skips when `xmllint` is absent**, so the make target fails loudly instead —
an interoperability check that silently skips silently rots. It covers both
directions: the shipped examples as authored, and every example re-written
through the writer, plus a model exported from Camunda.

The schemas are vendored rather than fetched. A validation gate that reaches
omg.org is a gate nobody trusts after the third false alarm.

## Conformance

| Level | What it admits | Verdict |
|---|---|---|
| **1** | Documentation only | n/a |
| **2** | S-FEEL: decision tables, simple unary tests, literal expressions | Supported; `conformance_level: "s-feel"` enforces it |
| **3** | Full FEEL and the whole boxed-expression family | Supported; the default |

Declaring level 2 is not decoration — the dialect gate rejects FEEL-only
constructs at load time with `VERDICT_LOAD_010`, so a model that claims to be
portable S-FEEL is held to it.

Java-bound and PMML function definitions are parsed and preserved through a
round trip, but not executed: calling one returns null and reports
`VERDICT_LOAD_007` or `VERDICT_LOAD_008` at load, and `VERDICT_EVAL_011` when
evaluated. A model that uses them loads and every other decision in it still
runs.
