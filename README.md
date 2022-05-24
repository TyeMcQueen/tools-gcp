# tools-gcp

This is a collection of libraries and utilities for dealing with GCP APIs.
Currently this includes CloudTrace and CloudMonitoring.

The [gcp2prom](doc/gcp2prom) command exports GCP metrics to Prometheus.
Releases are available as Docker containers for easy deployment to GKE.

The gcp-metrics command provides information about
GCP metrics, including which metrics actually have data in your project
and the ability to get raw metric data.

The mon module wraps the CloudMonitoring API for querying metrics.

The trace module implements CloudTrace span registration.
