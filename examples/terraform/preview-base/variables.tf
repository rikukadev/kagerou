variable "project" {
  description = "kagerou project name; namespaces every resource (must match kagerou.yaml)"
  type        = string

  validation {
    condition     = can(regex("^[a-z]([a-z0-9-]*[a-z0-9])?$", var.project))
    error_message = "project must match ^[a-z]([a-z0-9-]*[a-z0-9])?$ (same rule as kagerou.yaml)."
  }
}

variable "domain_name" {
  description = "This app's preview domain, e.g. todo.example.com (URLs become pr-42.todo.example.com)"
  type        = string
}

variable "hosted_zone_id" {
  description = "Route53 public hosted zone that owns the parent of domain_name"
  type        = string
}

variable "routing" {
  description = <<-EOT
    How to resolve extensionless paths (kagerou docs/CONTRACT.md §9).
    "directory" appends /index.html (static site generators emit /about/index.html).
    "spa" maps them all to the environment's /index.html; the file for /about does
    not exist, so "directory" would 403.
  EOT
  type        = string
  default     = "directory"

  validation {
    condition     = contains(["directory", "spa"], var.routing)
    error_message = "routing must be \"directory\" or \"spa\"."
  }
}
