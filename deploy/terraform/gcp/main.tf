# VoltSight on Google Cloud: VPC, GKE (private nodes), Cloud SQL for PostgreSQL (HA), Memorystore Redis, a CMEK
# key and a Cloud Storage cold tier. Kafka runs in the cluster via the Strimzi operator (see docs/deployment.md);
# the same Helm release as on AWS is used, only `config.*` differs.
terraform {
  required_version = ">= 1.6"
  required_providers {
    google = { source = "hashicorp/google", version = "~> 6.0" }
    random = { source = "hashicorp/random", version = "~> 3.6" }
  }
}

provider "google" {
  project = var.project_id
  region  = var.region
}

locals {
  name = "voltsight-${var.environment}"
}

resource "google_project_service" "apis" {
  for_each = toset(["container.googleapis.com", "sqladmin.googleapis.com", "redis.googleapis.com", "cloudkms.googleapis.com", "servicenetworking.googleapis.com"])
  service  = each.key
}

# ---------- encryption at rest (CMEK) ----------
resource "google_kms_key_ring" "main" {
  name     = local.name
  location = var.region
}

resource "google_kms_crypto_key" "data" {
  name            = "data"
  key_ring        = google_kms_key_ring.main.id
  rotation_period = "7776000s"
}

# ---------- network ----------
resource "google_compute_network" "main" {
  name                    = local.name
  auto_create_subnetworks = false
}

resource "google_compute_subnetwork" "main" {
  name                     = local.name
  region                   = var.region
  network                  = google_compute_network.main.id
  ip_cidr_range            = "10.50.0.0/20"
  private_ip_google_access = true
  secondary_ip_range {
    range_name    = "pods"
    ip_cidr_range = "10.60.0.0/14"
  }
  secondary_ip_range {
    range_name    = "services"
    ip_cidr_range = "10.64.0.0/20"
  }
  log_config {
    aggregation_interval = "INTERVAL_10_MIN"
    flow_sampling        = 0.5
  }
}

resource "google_compute_global_address" "private_services" {
  name          = "${local.name}-private-services"
  purpose       = "VPC_PEERING"
  address_type  = "INTERNAL"
  prefix_length = 16
  network       = google_compute_network.main.id
}

resource "google_service_networking_connection" "private" {
  network                 = google_compute_network.main.id
  service                 = "servicenetworking.googleapis.com"
  reserved_peering_ranges = [google_compute_global_address.private_services.name]
}

# ---------- Kubernetes ----------
resource "google_container_cluster" "main" {
  name                     = local.name
  location                 = var.region
  network                  = google_compute_network.main.id
  subnetwork               = google_compute_subnetwork.main.id
  remove_default_node_pool = true
  initial_node_count       = 1
  deletion_protection      = true
  ip_allocation_policy {
    cluster_secondary_range_name  = "pods"
    services_secondary_range_name = "services"
  }
  private_cluster_config {
    enable_private_nodes    = true
    enable_private_endpoint = !var.public_api_endpoint
    master_ipv4_cidr_block  = "172.16.0.0/28"
  }
  database_encryption {
    state    = "ENCRYPTED"
    key_name = google_kms_crypto_key.data.id
  }
  workload_identity_config { workload_pool = "${var.project_id}.svc.id.goog" }
  network_policy { enabled = true }
  addons_config {
    network_policy_config { disabled = false }
  }
  depends_on = [google_project_service.apis]
}

resource "google_container_node_pool" "general" {
  name       = "general"
  cluster    = google_container_cluster.main.id
  location   = var.region
  node_count = var.node_count
  autoscaling {
    min_node_count = 1
    max_node_count = var.node_max
  }
  node_config {
    machine_type = var.machine_type
    disk_type    = "pd-ssd"
    shielded_instance_config {
      enable_secure_boot          = true
      enable_integrity_monitoring = true
    }
    workload_metadata_config { mode = "GKE_METADATA" }
  }
  management {
    auto_repair  = true
    auto_upgrade = true
  }
}

# ---------- PostgreSQL ----------
resource "random_password" "db" {
  length  = 32
  special = false
}

resource "google_sql_database_instance" "postgres" {
  name                = "${local.name}-pg"
  database_version    = "POSTGRES_16"
  region              = var.region
  encryption_key_name = google_kms_crypto_key.data.id
  deletion_protection = true
  settings {
    tier              = var.db_tier
    availability_type = "REGIONAL"
    disk_autoresize   = true
    disk_type         = "PD_SSD"
    backup_configuration {
      enabled                        = true
      point_in_time_recovery_enabled = true
      backup_retention_settings { retained_backups = 14 }
    }
    ip_configuration {
      ipv4_enabled    = false
      private_network = google_compute_network.main.id
      ssl_mode        = "ENCRYPTED_ONLY"
    }
    database_flags {
      name  = "cloudsql.enable_pgaudit"
      value = "on"
    }
  }
  depends_on = [google_service_networking_connection.private]
}

resource "google_sql_database" "voltsight" {
  name     = "voltsight"
  instance = google_sql_database_instance.postgres.name
}

resource "google_sql_user" "voltsight" {
  name     = "voltsight"
  instance = google_sql_database_instance.postgres.name
  password = random_password.db.result
}

# ---------- Redis ----------
resource "google_redis_instance" "state" {
  name                    = "${local.name}-redis"
  tier                    = "STANDARD_HA"
  memory_size_gb          = var.redis_gb
  region                  = var.region
  authorized_network      = google_compute_network.main.id
  transit_encryption_mode = "SERVER_AUTHENTICATION"
  auth_enabled            = true
  customer_managed_key    = google_kms_crypto_key.data.id
  depends_on              = [google_service_networking_connection.private]
}

# ---------- object storage ----------
resource "google_storage_bucket" "cold" {
  name                        = "${var.project_id}-${local.name}-cold"
  location                    = var.region
  uniform_bucket_level_access = true
  public_access_prevention    = "enforced"
  versioning { enabled = true }
  encryption { default_kms_key_name = google_kms_crypto_key.data.id }
  lifecycle_rule {
    condition { age = 30 }
    action {
      type          = "SetStorageClass"
      storage_class = "NEARLINE"
    }
  }
  lifecycle_rule {
    condition { age = 180 }
    action {
      type          = "SetStorageClass"
      storage_class = "COLDLINE"
    }
  }
}
