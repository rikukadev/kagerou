# kagerou preview base の Terraform 版。CFN 版(deploy/preview-base.yaml)と
# 同じ構成・同じ SSM データ契約(CONTRACT §9)。us-east-1 で適用すること。

terraform {
  required_providers {
    aws = {
      source  = "hashicorp/aws"
      version = ">= 5.0"
    }
  }
}

data "aws_caller_identity" "current" {}

locals {
  bucket_name = "kagerou-base-${var.project}-${data.aws_caller_identity.current.account_id}"
  # Managed-CachingDisabled(プレビューは正しさ優先・invalidation 不要)
  caching_disabled_policy_id = "4135ea2d-6df8-44a3-9df3-4b5a84be39ad"
  cloudfront_hosted_zone_id  = "Z2FDTNDATAQYW2" # CloudFront alias の固定ゾーン
}

# --- 成果物バケット(非公開、OAC 経由のみ)---------------------------------

resource "aws_s3_bucket" "base" {
  bucket = local.bucket_name
}

resource "aws_s3_bucket_public_access_block" "base" {
  bucket                  = aws_s3_bucket.base.id
  block_public_acls       = true
  block_public_policy     = true
  ignore_public_acls      = true
  restrict_public_buckets = true
}

# --- ワイルドカード証明書(DNS 検証)---------------------------------------

resource "aws_acm_certificate" "wildcard" {
  domain_name       = "*.${var.domain_name}"
  validation_method = "DNS"

  lifecycle {
    create_before_destroy = true
  }
}

resource "aws_route53_record" "cert_validation" {
  for_each = {
    for dvo in aws_acm_certificate.wildcard.domain_validation_options : dvo.domain_name => {
      name   = dvo.resource_record_name
      type   = dvo.resource_record_type
      record = dvo.resource_record_value
    }
  }

  zone_id = var.hosted_zone_id
  name    = each.value.name
  type    = each.value.type
  ttl     = 60
  records = [each.value.record]
}

resource "aws_acm_certificate_validation" "wildcard" {
  certificate_arn         = aws_acm_certificate.wildcard.arn
  validation_record_fqdns = [for r in aws_route53_record.cert_validation : r.fqdn]
}

# --- CloudFront -------------------------------------------------------------

resource "aws_cloudfront_origin_access_control" "base" {
  name                              = "kagerou-base-${var.project}"
  origin_access_control_origin_type = "s3"
  signing_behavior                  = "always"
  signing_protocol                  = "sigv4"
}

# Host の最初のラベル(pr-42.todo.example.com → pr-42)を S3 プレフィックスに写す。
# 拡張子の無いパスの扱いは routing で切り替える(CFN 版と同一コード)。
resource "aws_cloudfront_function" "host_to_prefix" {
  name    = "kagerou-base-${var.project}-host-to-prefix"
  runtime = "cloudfront-js-2.0"
  publish = true
  comment = "map <name>.<domain>/<uri> to /<name>/<uri> (routing: ${var.routing})"
  code    = <<-EOT
    var ROUTING = '${var.routing}';

    function handler(event) {
      var req = event.request;
      var name = req.headers.host.value.split('.')[0];
      var uri = req.uri;
      if (uri.endsWith('/')) {
        uri += 'index.html';
      } else if (!uri.split('/').pop().includes('.')) {
        // 拡張子の無いパス。SSG は /about/index.html を出すが、SPA では
        // その実体が無く、OAC 経由の S3 は 404 ではなく 403 を返す。
        uri = ROUTING === 'spa' ? '/index.html' : uri + '/index.html';
      }
      req.uri = '/' + name + uri;
      return req;
    }
  EOT
}

resource "aws_cloudfront_distribution" "base" {
  enabled      = true
  comment      = "kagerou preview base (${var.project})"
  http_version = "http2"
  aliases      = ["*.${var.domain_name}"]

  viewer_certificate {
    acm_certificate_arn      = aws_acm_certificate_validation.wildcard.certificate_arn
    ssl_support_method       = "sni-only"
    minimum_protocol_version = "TLSv1.2_2021"
  }

  origin {
    origin_id                = "bucket"
    domain_name              = aws_s3_bucket.base.bucket_regional_domain_name
    origin_access_control_id = aws_cloudfront_origin_access_control.base.id
  }

  default_cache_behavior {
    target_origin_id       = "bucket"
    viewer_protocol_policy = "redirect-to-https"
    allowed_methods        = ["GET", "HEAD"]
    cached_methods         = ["GET", "HEAD"]
    cache_policy_id        = local.caching_disabled_policy_id

    function_association {
      event_type   = "viewer-request"
      function_arn = aws_cloudfront_function.host_to_prefix.arn
    }
  }

  restrictions {
    geo_restriction {
      restriction_type = "none"
    }
  }
}

resource "aws_s3_bucket_policy" "base" {
  bucket = aws_s3_bucket.base.id
  policy = jsonencode({
    Version = "2012-10-17"
    Statement = [{
      Effect    = "Allow"
      Principal = { Service = "cloudfront.amazonaws.com" }
      Action    = "s3:GetObject"
      Resource  = "${aws_s3_bucket.base.arn}/*"
      Condition = {
        StringEquals = {
          "AWS:SourceArn" = aws_cloudfront_distribution.base.arn
        }
      }
    }]
  })
}

resource "aws_route53_record" "wildcard" {
  zone_id = var.hosted_zone_id
  name    = "*.${var.domain_name}"
  type    = "A"

  alias {
    name                   = aws_cloudfront_distribution.base.domain_name
    zone_id                = local.cloudfront_hosted_zone_id
    evaluate_target_health = false
  }
}

# --- ベース↔環境のデータ契約(CONTRACT §9)---------------------------------
# kagerou init はこの SSM キーで既存ベースを検出する。CFN 版と同一のキー。

resource "aws_ssm_parameter" "domain" {
  name  = "/kagerou/base/${var.project}/domain"
  type  = "String"
  value = var.domain_name
}

resource "aws_ssm_parameter" "bucket" {
  name  = "/kagerou/base/${var.project}/bucket"
  type  = "String"
  value = aws_s3_bucket.base.id
}

resource "aws_ssm_parameter" "distribution" {
  name  = "/kagerou/base/${var.project}/distribution"
  type  = "String"
  value = aws_cloudfront_distribution.base.id
}

# ベースがどちらのモードで配るかは、あとから見て分かる必要がある
# (ディープリンクが 403 になったとき、まずここを見れば切り分けられる)。
resource "aws_ssm_parameter" "routing" {
  name  = "/kagerou/base/${var.project}/routing"
  type  = "String"
  value = var.routing
}
