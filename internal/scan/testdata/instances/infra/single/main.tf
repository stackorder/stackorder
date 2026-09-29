terraform {
  backend "s3" {
    key = "single.tfstate"
  }
}
