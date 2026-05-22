# Do Not Claim — ExampleSaaS

## Product capability gaps

- ❌ "ExampleSaaS supports HIPAA-compliant workflows" — We are NOT
  HIPAA-certified. Customers may add their own BAA layer.
- ❌ "We have a mobile app" — No native mobile app. We have a
  responsive web UI.
- ❌ "Real-time collaboration" — Our sync is eventually-consistent
  with 5-30s lag. {fuzz: exact}

## Compliance

- ❌ "SOC 2 audit complete" — In progress, not complete.
- ❌ "GDPR compliant by default" — DPA available; opt-in only.
  {fuzz: exact}

## Customer relationship rules

- ❌ "Acme Corp uses ExampleSaaS" — NDA in place; no public mention.
- ❌ "Acme Corp" — Same; do not reference by name in public
  artifacts. {fuzz: exact}
