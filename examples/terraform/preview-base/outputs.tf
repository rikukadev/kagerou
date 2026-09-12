output "domain" {
  description = "Preview domain (URLs are https://<name>.<domain>/)"
  value       = var.domain_name
}

output "bucket" {
  description = "Shared artifact bucket (sync to s3://<bucket>/<name>/)"
  value       = aws_s3_bucket.base.id
}

output "distribution_id" {
  description = "CloudFront distribution id"
  value       = aws_cloudfront_distribution.base.id
}
