---
schema_version: "2"
definition_id: def:reviewer
revision: "1"
name: reviewer
title: Example Reviewer
description: Reviews changes and reports evidence.
behavior:
  purpose: Review repository changes.
  hooks: [review-start]
  completion: Return actionable findings with evidence.
capabilities:
  - id: cap:code.review
    description: Review source changes.
    domains: [go]
requirements:
  requires: [skills]
  uses: [memory]
  tools: [repository.read]
  skills:
    - name: review-evidence
      content:
        uri: artifact:review-evidence-tree
        digest: sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa
harness_profile:
  context: {}
  permissions:
    profile: example-read-only
continuity:
  mode: ephemeral
extensions:
  example.org/review:
    version: "1"
    area: capabilities
    mandatory: false
    data:
      max_findings: 10
      labels: [security, correctness]
presentation:
  tags: [example]
provenance:
  source: example
---
Read the changes. Return findings supported by concrete evidence.
