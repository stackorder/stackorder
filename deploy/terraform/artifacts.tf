resource "aws_s3_bucket" "artifacts" {
  count = var.artifact_bucket_enabled ? 1 : 0

  bucket = "${var.name}-artifacts-${local.account_id}-${local.region}"

  tags = var.tags
}

resource "aws_s3_bucket_ownership_controls" "artifacts" {
  count = var.artifact_bucket_enabled ? 1 : 0

  bucket = aws_s3_bucket.artifacts[0].id

  rule {
    object_ownership = "BucketOwnerEnforced"
  }
}

resource "aws_s3_bucket_public_access_block" "artifacts" {
  count = var.artifact_bucket_enabled ? 1 : 0

  bucket                  = aws_s3_bucket.artifacts[0].id
  block_public_acls       = true
  block_public_policy     = true
  ignore_public_acls      = true
  restrict_public_buckets = true
}

resource "aws_s3_bucket_server_side_encryption_configuration" "artifacts" {
  count = var.artifact_bucket_enabled ? 1 : 0

  bucket = aws_s3_bucket.artifacts[0].id

  rule {
    apply_server_side_encryption_by_default {
      sse_algorithm = "AES256"
    }
  }
}

resource "aws_s3_bucket_versioning" "artifacts" {
  count = var.artifact_bucket_enabled ? 1 : 0

  bucket = aws_s3_bucket.artifacts[0].id

  versioning_configuration {
    status = "Disabled"
  }
}

resource "aws_s3_bucket_lifecycle_configuration" "artifacts" {
  count = var.artifact_bucket_enabled ? 1 : 0

  bucket = aws_s3_bucket.artifacts[0].id

  rule {
    id     = "expire-artifacts"
    status = "Enabled"

    filter {}

    expiration {
      days = var.artifact_retention_days
    }

    abort_incomplete_multipart_upload {
      days_after_initiation = 1
    }
  }
}

resource "aws_s3_bucket_policy" "artifacts" {
  count = var.artifact_bucket_enabled ? 1 : 0

  bucket = aws_s3_bucket.artifacts[0].id
  policy = jsonencode({
    Version = "2012-10-17"
    Statement = [{
      Sid       = "DenyInsecureTransport"
      Effect    = "Deny"
      Principal = "*"
      Action    = "s3:*"
      Resource  = [aws_s3_bucket.artifacts[0].arn, "${aws_s3_bucket.artifacts[0].arn}/*"]
      Condition = {
        Bool = { "aws:SecureTransport" = "false" }
      }
    }]
  })

  depends_on = [aws_s3_bucket_public_access_block.artifacts]
}
