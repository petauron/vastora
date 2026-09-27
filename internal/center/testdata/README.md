# Catalog consumer test fixtures

- `catalog-v4.json` is a minimal synthetic generic application contract fixture.
- `reviewed-catalog-v4.json` is a frozen six-application integration snapshot
  copied from the schema 4 implementation of `petauron/catalog`. It exists only
  to exercise product wiring, artifacts and migrations in Vastora tests/CI.

Neither file is an authoritative catalog or a distribution input. Application
releases do not update these files automatically. The independent catalog
repository owns reviewed recipes, schema, provenance and publication.
