# stacks/prod/vpc

Production network: the root of the production graph, built from `modules/vpc`; `stacks/prod/eks` depends on it and `stacks/prod/apps` reads its state.
