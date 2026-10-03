---
schema_version: "2"
definition_id: def:coordinator
revision: "3"
name: coordinator
description: Coordinates a durable example role.
behavior:
  purpose: Coordinate work and preserve decisions.
  instructions:
    - uri: artifact:coordinator-instructions
      digest: sha256:bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb
requirements: {}
harness_profile:
  context:
    policy:
      uri: policy:context
      digest: sha256:cccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccc
  permissions:
    profile: example-bounded
continuity:
  mode: durable
  memory_policy:
    uri: policy:memory
    digest: sha256:eeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeee
  recovery_strategy:
    uri: policy:recovery
    digest: sha256:ffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffff
---
