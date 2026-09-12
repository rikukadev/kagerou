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
