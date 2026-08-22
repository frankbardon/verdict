# DMN 1.3 XML Schemas

Vendored verbatim from the OMG, for offline schema validation in tests.

| File | Source |
|---|---|
| `DMN13.xsd` | https://www.omg.org/spec/DMN/20191111/DMN13.xsd |
| `DMNDI13.xsd` | https://www.omg.org/spec/DMN/20191111/DMNDI13.xsd |
| `DC.xsd` | https://www.omg.org/spec/DMN/20180521/DC.xsd |
| `DI.xsd` | https://www.omg.org/spec/DMN/20180521/DI.xsd |

They are here so `TestExamplesValidateAgainstTheDMNSchema` can run in CI without
network access. A validation test that fetches from omg.org is a test that fails
when omg.org is slow, and one nobody trusts after the third false alarm.

DMN 1.3 is the version validated against because it is what the mainstream
tooling reads — Camunda Modeler and dmn-js both target it. Verdict's reader
accepts 1.3, 1.4 and 1.5; its writer emits 1.3 by default for exactly this
reason.

Do not edit these files. To update, re-download from the URLs above.
