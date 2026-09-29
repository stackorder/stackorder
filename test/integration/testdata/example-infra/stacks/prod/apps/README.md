# stacks/prod/apps

Production applications; reads the VPC from the `stacks/prod/vpc` state with `terraform_remote_state` and has no `.stackorder.yaml`, so its dependency is inferred.
