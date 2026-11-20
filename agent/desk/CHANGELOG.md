# Changelog

## [Unreleased]

## [0.1.0] - 2026-11-20

- New module: an agent's front desk, host-neutral and without dependencies.
  - `Ledger`: one `Topic` per open line of the owner's business, with
    work (`Work`, `WorkState`), questions to the owner (`Question`) and
    related refs (`Ref`). `Find` routes a finished job back to its topic.
    JSON round trip for persistence.
  - `Editor`, the delivery editor: `Item`s (result, failure, question,
    progress, notice) are decided by a `Policy` to go out now, merge on
    their topic, hold while the owner answers a question on the topic,
    join a digest, or be filed without a message. `Due` releases what is
    ready as one composed `Message`; `Next` says when to call again.
    Digest items ride along with a release unless the policy is `Quiet`;
    a busy owner only gets critical items and questions until `BusyMax`.
    Critical items and owner questions always go out now, whatever the
    rules say. `DefaultPolicy` ships sane defaults; `Layer` puts app and
    module policies over it.
  - Intake: `Screen` reads a message with rules; `Rules` and `Model`
    decomposers turn it into a `Plan` of typed work items with
    dependencies (`Waves`), validated, with a plan `Card`. `Intake` runs
    off, shadow, suggest or auto, falling back to the rules' plan when the
    model fails. `Correction` records the owner's answer to a suggested
    plan.
