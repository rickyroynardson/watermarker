# Partial configuration: initialize with backend.s3.tfbackend after bootstrapping the bucket.
terraform {
  backend "s3" {}
}
