# stacks/legacy/dns

Legacy private DNS zone; reads `stacks/prod/vpc` state but suppresses the inferred edge with `ignore_inferred`, and matches no `environments` prefix, so it runs in the `default` environment.
