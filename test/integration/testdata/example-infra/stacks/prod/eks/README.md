# stacks/prod/eks

Production Kubernetes cluster from `modules/eks`; reads the VPC id and subnets from the outputs of `stacks/prod/vpc` with the `stackorder_outputs` data source and has no `.stackorder.yaml`, so its dependency is inferred.
