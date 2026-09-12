module github.com/frankbardon/verdict/nexus

go 1.26.1

require (
	github.com/frankbardon/nexus v0.20.0
	github.com/frankbardon/verdict v0.1.0
)

require (
	github.com/dustin/go-humanize v1.0.1 // indirect
	github.com/google/go-cmp v0.7.0 // indirect
	github.com/google/uuid v1.6.0 // indirect
	github.com/klauspost/compress v1.19.2 // indirect
	github.com/mattn/go-isatty v0.0.24 // indirect
	github.com/mitchellh/mapstructure v1.5.0 // indirect
	github.com/ncruces/go-strftime v1.0.0 // indirect
	github.com/remyoudompheng/bigfft v0.0.0-20230129092748-24d4a6f8daec // indirect
	github.com/santhosh-tekuri/jsonschema/v6 v6.0.3 // indirect
	golang.org/x/sys v0.47.0 // indirect
	golang.org/x/text v0.41.0 // indirect
	gopkg.in/yaml.v3 v3.0.1 // indirect
	modernc.org/libc v1.74.4 // indirect
	modernc.org/mathutil v1.7.1 // indirect
	modernc.org/memory v1.11.0 // indirect
	modernc.org/sqlite v1.56.0 // indirect
)

// Verdict's core and its Nexus integration live in one repository but are two
// Go modules, so that importing the engine never drags in Nexus. During
// development the core is taken from the working tree; a published tag
// satisfies the require above for downstream consumers, for whom this replace
// is inert (a replace directive only applies to the main module).
replace github.com/frankbardon/verdict => ../
