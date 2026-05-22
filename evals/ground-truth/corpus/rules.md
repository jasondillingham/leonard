# Forbidden-claim rules for the FP corpus

The fuzzy matcher (#13) is measured against the inputs in
forbidden.txt / safe.txt using these rules. Each rule uses
DefaultFuzzThreshold unless annotated.

## Product capability gaps

- ❌ "ExampleSaaS supports HIPAA-compliant workflows" — Not HIPAA-certified.
- ❌ "We have a mobile app" — Web only.
- ❌ "Real-time collaboration" — Eventually-consistent with 5-30s lag.

## Compliance

- ❌ "SOC 2 audit complete" — In progress, not complete.
- ❌ "GDPR compliant by default" {fuzz: exact}
