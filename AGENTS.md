# Repository Agent Instructions

This repository is a personal fork of the upstream VoCat project and contains fork-specific functionality that must be preserved.

## Fork policy

- Treat this repository as an independently maintained fork, not as a staging branch for upstream contributions.
- Preserve the fork-specific features and behavior already present in this repository.
- When updating from upstream, integrate the latest upstream fixes and features while keeping this fork's custom functionality intact.
- Resolve upstream conflicts in favor of preserving both upstream improvements and the intended fork-specific behavior whenever possible.
- Do not remove, overwrite, or regress fork-specific functionality merely to make the tree match upstream.
- Do not open or prepare pull requests from this fork back to the upstream repository unless the repository owner explicitly asks for that in the current task.
- Do not assume a change should be upstreamed. Changes requested here are for this fork by default.

## Upstream sync guidance

When asked to sync with upstream:

1. Identify the upstream repository and fetch its latest default branch.
2. Review upstream changes before applying them.
3. Merge or rebase upstream changes into this fork carefully.
4. Keep fork-specific commits/features unless the owner explicitly asks to remove them.
5. Run relevant tests/build checks after conflict resolution.
6. Summarize any conflicts or behavior changes introduced by the sync.

## Default intent

For future development tasks, optimize for:

- maintaining this fork as its own product;
- staying reasonably current with upstream;
- minimizing unnecessary divergence where practical;
- preserving the user's independent features;
- never sending this fork's changes upstream by PR without explicit instruction.
