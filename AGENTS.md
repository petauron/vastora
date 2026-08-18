# Vastora Agent Rules

- Do not preserve obsolete API or implementation compatibility. Remove superseded implementations outright; do not add aliases, dual code paths, or runtime fallbacks.
- Released Center database schemas must use tested, forward-only migrations. Back up before migrating, fail closed on migration errors, and do not support automatic database downgrade.
- Choose the simplest implementation that fully satisfies the current requirements. Avoid speculative abstractions, unnecessary indirection, and redundant configuration layers.
- Build the system incrementally in vertical slices. Make the smallest end-to-end version work before adding more layers, and never dismantle working functionality to accommodate unfinished complexity.
- Keep components modular and maintain a clear separation of concerns.
- Prefer mature, actively maintained libraries. Do not reimplement established functionality without a clear, documented reason.
- Inspect the capabilities of existing project dependencies before adding a new package or writing custom code. Do not assume the required capability is missing.
- Make architectural decisions for the long term. Do not introduce temporary designs with the intention of replacing them later.
- Study how mature products solve the same problem and follow proven patterns instead of inventing a solution from scratch.
