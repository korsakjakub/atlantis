# N. Short title

Date: YYYY-MM-DD

## Status

Proposed

<!-- Proposed, Accepted, Rejected, Superseded by [NNNN](NNNN-short-title.md), or Deprecated. -->

## Context

Describe the problem and constraints that motivate this fork-only change, and why it is not suited for upstream (or link the upstream issue or PR).

## Decision

State what the fork will do and where the code lives (`server/fork/<feature>/`, plus any upstream hook-up points).

## Consequences

State the benefits and tradeoffs, including the upstream-sync maintenance cost (files that will conflict).

## Feature flag

- Flag: `--fork-<name>`
- Env var: `ATLANTIS_FORK_<NAME>`
- Default: off
