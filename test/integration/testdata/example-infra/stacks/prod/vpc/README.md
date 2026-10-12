# stacks/prod/vpc

Production network: the root of the production graph, built from `modules/vpc`; it shares its outputs with `stacks/prod/eks` through `outputs.readers` in `.stackorder.yaml`, and `stacks/prod/apps` reads its state.
