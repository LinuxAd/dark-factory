# Architecture decision records

These are short records of the decisions that shape this repository, written when each decision was made. Each record gives:

- its status;
- the context;
- the decision;
- the consequences, including the downsides;
- the alternatives, where they matter.

A reversed decision gets a new record that supersedes the old one. The old record is not rewritten.

Each phase starts with an ADR (SPEC.md §1). Research that an ADR relies on goes in `docs/research/`.

| Number | Decision |
|--------|----------|
| [0001](0001-phase0-foundations.md) | Phase 0 foundations: gateway, CloudNativePG with the Barman Cloud plugin and always-recovery bring-up, Smokescreen egress, a sandbox namespace with enforced NetworkPolicy, and a pre-teardown backup in platform-project |
