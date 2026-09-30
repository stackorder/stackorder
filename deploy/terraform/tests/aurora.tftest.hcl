mock_provider "aws" {
  override_during = plan
  source          = "./tests/mocks/aws"
}

mock_provider "http" {
  override_during = plan
  source          = "./tests/mocks/http"
}

override_resource {
  target          = aws_secretsmanager_secret.database
  override_during = plan
  values = {
    arn = "arn:aws:secretsmanager:eu-west-1:123456789012:secret:stackorder/database-url-AbCdEf"
  }
}

variables {
  domain_name           = "stackorder.example.com"
  route53_zone_id       = "Z0123456789ABCDEFGHIJ"
  use_aurora_serverless = true
}

run "serverless_cluster" {
  command = plan

  variables {
    aurora_min_acu = 1
    aurora_max_acu = 4
  }

  assert {
    condition     = length(aws_db_instance.this) == 0 && length(aws_db_parameter_group.this) == 0
    error_message = "The RDS instance path must be off with Aurora."
  }

  assert {
    condition = (
      aws_rds_cluster.this[0].engine == "aurora-postgresql" &&
      aws_rds_cluster.this[0].engine_mode == "provisioned" &&
      aws_rds_cluster.this[0].engine_version == "17.5"
    )
    error_message = "The cluster must be provisioned Aurora PostgreSQL at the resolved 17.x version."
  }

  assert {
    condition     = data.aws_rds_engine_version.aurora[0].version == "17" && data.aws_rds_engine_version.aurora[0].default_only
    error_message = "A major-only engine_version must resolve to the default minor of that major."
  }

  assert {
    condition = (
      one(aws_rds_cluster.this[0].serverlessv2_scaling_configuration).min_capacity == 1 &&
      one(aws_rds_cluster.this[0].serverlessv2_scaling_configuration).max_capacity == 4
    )
    error_message = "The serverless v2 capacity range must follow the ACU inputs."
  }

  assert {
    condition = (
      aws_rds_cluster.this[0].storage_encrypted &&
      aws_rds_cluster.this[0].deletion_protection &&
      aws_rds_cluster.this[0].final_snapshot_identifier == "stackorder-final" &&
      aws_rds_cluster.this[0].database_name == "stackorder"
    )
    error_message = "The cluster must be encrypted, protected and keep a final snapshot."
  }

  assert {
    condition = (
      aws_rds_cluster_parameter_group.this[0].family == "aurora-postgresql17" &&
      one([for p in aws_rds_cluster_parameter_group.this[0].parameter : p.value if p.name == "rds.force_ssl"]) == "1"
    )
    error_message = "The cluster parameter group must force TLS."
  }

  assert {
    condition = (
      length(aws_rds_cluster_instance.this) == 1 &&
      aws_rds_cluster_instance.this[0].instance_class == "db.serverless" &&
      aws_rds_cluster_instance.this[0].identifier == "stackorder-1" &&
      !aws_rds_cluster_instance.this[0].publicly_accessible
    )
    error_message = "One private db.serverless writer must be created."
  }

  assert {
    condition     = can(regex("^postgres://stackorder:[0-9A-Za-z]{40}@stackorder\\.cluster-abcdefghijkl\\.eu-west-1\\.rds\\.amazonaws\\.com:5432/stackorder\\?sslmode=require$", local.database_url))
    error_message = "DATABASE_URL must point at the cluster writer endpoint."
  }

  assert {
    condition = (
      aws_rds_cluster.this[0].master_password == null &&
      aws_rds_cluster.this[0].master_password_wo == null &&
      aws_rds_cluster.this[0].master_password_wo_version == parseint(substr(sha256(jsonencode([1, "arn:aws:secretsmanager:eu-west-1:123456789012:secret:stackorder/database-url-AbCdEf"])), 0, 12), 16)
    )
    error_message = "The cluster password must be write-only, versioned by db_password_version and the database secret."
  }

  assert {
    condition     = aws_secretsmanager_secret_version.database.secret_string_wo_version == parseint(substr(sha256(jsonencode([1, "cluster-ABCDEFGHIJKLMNOPQRSTUVWXYZ"])), 0, 12), 16)
    error_message = "The database secret must be rewritten when a new cluster is created, since the cluster gets the password generated in that apply."
  }

  assert {
    condition     = output.db_endpoint == "stackorder.cluster-abcdefghijkl.eu-west-1.rds.amazonaws.com"
    error_message = "db_endpoint must be the cluster writer endpoint."
  }

  assert {
    condition = (
      !aws_rds_cluster.this[0].allow_major_version_upgrade &&
      !aws_rds_cluster.this[0].apply_immediately &&
      !aws_rds_cluster_instance.this[0].apply_immediately
    )
    error_message = "Major version upgrades must be refused and changes wait for the maintenance window by default."
  }
}

run "serverless_major_version_upgrade" {
  command = plan

  variables {
    multi_az                    = true
    allow_major_version_upgrade = true
    apply_immediately           = true
  }

  assert {
    condition     = aws_rds_cluster.this[0].allow_major_version_upgrade && aws_rds_cluster.this[0].apply_immediately
    error_message = "allow_major_version_upgrade and apply_immediately must reach the cluster."
  }

  assert {
    condition     = alltrue([for i in aws_rds_cluster_instance.this : i.apply_immediately])
    error_message = "apply_immediately must reach every cluster instance."
  }
}

run "serverless_multi_az" {
  command = plan

  variables {
    multi_az            = true
    deletion_protection = false
    skip_final_snapshot = true
    alarms_enabled      = true
    alarm_sns_topic_arn = "arn:aws:sns:eu-west-1:123456789012:alerts"
  }

  assert {
    condition     = [for i in aws_rds_cluster_instance.this : i.identifier] == ["stackorder-1", "stackorder-2"]
    error_message = "multi_az must add a reader instance."
  }

  assert {
    condition     = [for i in aws_rds_cluster_instance.this : i.promotion_tier] == [0, 1]
    error_message = "The writer must have the highest promotion priority."
  }

  assert {
    condition     = !aws_rds_cluster.this[0].deletion_protection && aws_rds_cluster.this[0].final_snapshot_identifier == null
    error_message = "deletion_protection and skip_final_snapshot must follow the variables."
  }

  assert {
    condition = (
      length(aws_cloudwatch_metric_alarm.db_free_storage) == 0 &&
      alltrue([for a in aws_cloudwatch_metric_alarm.db_capacity : a.metric_name == "ACUUtilization" && a.threshold == 90]) &&
      [for a in aws_cloudwatch_metric_alarm.db_capacity : a.dimensions.DBInstanceIdentifier] == ["stackorder-1", "stackorder-2"]
    )
    error_message = "Each Aurora instance must get an ACU utilization alarm instead of a free storage alarm."
  }
}

run "serverless_pinned_minor" {
  command = plan

  variables {
    engine_version = "16.6"
  }

  assert {
    condition     = length(data.aws_rds_engine_version.aurora) == 0 && aws_rds_cluster.this[0].engine_version == "16.6"
    error_message = "A major.minor engine_version must be used as is."
  }

  assert {
    condition     = aws_rds_cluster_parameter_group.this[0].family == "aurora-postgresql16"
    error_message = "The parameter group family must follow the major version."
  }
}

run "serverless_max_below_min" {
  command = plan

  variables {
    aurora_min_acu = 4
    aurora_max_acu = 2
  }

  expect_failures = [var.aurora_max_acu]
}

run "serverless_min_below_half_acu" {
  command = plan

  variables {
    aurora_min_acu = 0.25
  }

  expect_failures = [var.aurora_min_acu]
}
