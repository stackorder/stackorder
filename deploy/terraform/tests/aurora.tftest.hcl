mock_provider "aws" {
  override_during = plan
  source          = "./tests/mocks/aws"
}

mock_provider "random" {
  override_during = plan
  source          = "./tests/mocks/random"
}

mock_provider "http" {
  override_during = plan
  source          = "./tests/mocks/http"
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
    condition     = data.aws_rds_engine_version.aurora[0].version == "17" && data.aws_rds_engine_version.aurora[0].latest
    error_message = "The engine version must resolve the latest minor of engine_version."
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
    condition     = nonsensitive(aws_secretsmanager_secret_version.database.secret_string) == "postgres://stackorder:mockdatabasepassword@stackorder.cluster-abcdefghijkl.eu-west-1.rds.amazonaws.com:5432/stackorder?sslmode=require"
    error_message = "DATABASE_URL must point at the cluster writer endpoint."
  }

  assert {
    condition     = output.db_endpoint == "stackorder.cluster-abcdefghijkl.eu-west-1.rds.amazonaws.com"
    error_message = "db_endpoint must be the cluster writer endpoint."
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
