# Deploying gcp2prom

Download [gcp2prom.yaml](gcp2prom.yaml).  Or download the other
YAML files in this directory (especially if using Kustomize).

Make adjustments based on how your Prometheus is set to discover scrape
targets.  The sample YAML file includes pod annotations that would often
accomplish this with older deployments of Prometheus as well as a
servicemonitor object that might work with a Prometheus Operator
deployment.  But how Prometheus discovers targets to scrape in Kubernetes
can be configured in so many different ways so neither of these approaches
may work in your environment.  But it is likely that one or the other only
needs minor tweaks.

Authenticate to your Kubernetes cluster, likely via:

    gcloud container clusters get-credentials $CLUSTER --project $PROJECT

Run

    kubectl apply -f gcp2prom.yaml

## Permissions

Your default service account for your nodes in any GKE node pool should have
permission to read GCP metrics, as recommended in [Hardening your cluster](
https://cloud.google.com/kubernetes-engine/docs/how-to/hardening-your-cluster#use_least_privilege_sa).

But you can configure workload identity to have gcp2prom use a different
service account for one of the following reasons:

* You don't want the node service account to have permission to read
    GCP metrics despite GCP's recommendation.
* You don't want gcp2prom to have some other access that is granted
    to your node service account.
* You want to use gcp2prom to read metrics from a GCP project other
    than the one the GKE cluster is in.

## Scraping Metrics From Other GCP Projects

Each deployment of gcp2prom can only read metrics from a single GCP project.
But you can have several deployments if you want to read metrics from
multiple GCP projects using a single GKE cluster.  Two changes are required
to accomplish this:

* Pass the GCP project ID as the first argument to the gcp2prom command.

* Use workload identity to provide that deployment of gcp2prom with a
    service account that has access to read metrics for the desired project.
    You can even create a single service account that has access to read
    metrics from many projects and use that one SA with each gcp2prom
    deployment.

## Configuring

The majority of configuration for gcp2prom is constructed by downloading
information about metrics available from GCP and adjusting that based on
the [gcp2prom.yaml](/gcp2prom.yaml) file that is included in the container.
In the unlikely event that you need to exclude some metrics from being
exported, then you can use command-line options or environment variables to
do this (see [gcp2prom.go](/cmd/gcp2prom/gcp2prom.go) for those details).

If there are GCP metrics that you want to export that are not currently
supported, then you'll need to update or replace the gcp2prom.yaml file
in the container.  It is easy to build a new container to replace that
YAML file.  You can also use a command-line option or environment
variable to have a different YAML file used.  Or you can file a github
Issue requesting support for those metrics be added.

You can change the value for LAGER_LEVELS (pod environment variable)
if you want a bunch of logs that are likely only of interest to people
working on changes to gcp2prom.  Setting it to "FWNAITDOG" enables all
logs.  Since GCP represents log level as a numeric "severity", you'll
want to look at the source code to see which letters correspond to
which logs.

If you aren't using GCP Cloud Logging, then you can remove the LAGER_GCP
environment variable and probably add a LAGER_KEYS environment variable
(with a value like "time,level,msg,data,,module") to better suit your log
aggregator.  See https://github.com/TyeMcQueen/go-lager for details.

### Configuration Changes To Avoid

Do not change how many replicas are run for the deployment.  That just
wastes resources and creates duplicate metrics.  After years, we have yet
to experience a scenario where having another replica would have improved
reliability.  Failures either were very quickly resolved by GKE restarting
the container or are more global (like a problem with GCP metrics) and
would have been experienced by all replicas.  GCP metric failures tend to
not be isolated to a single region, for example.  And the way Prometheus
deals with metrics means that there is no reasonable way to use multiple
replicas.  Only once have we experienced a failure that was noticeable
in the exported metrics.

Unfortunately, only one of the available options for image pull policy
is reasonable here: IfNotPresent.  Using Always or Never likely means
you shouldn't or can't use Docker Hub directly.  If there were a policy
that just checked the date of the image even once per week and would
pull the image if it had been updated, then we could use more generic
image tags like v1 or v1.2 such that you could get an update by just
forcing a pod restart.  We create such generic tags but they just are
not very useful as the image version in your Kubernetes YAML.

### Missing Metrics

The current version of gcp2prom does not pull metrics that it knows how
to export if there are no recent values for that metric type at the time
that gcp2prom started running.  So, if you enable a new GCP feature that
produces metrics that you would like exported, you will probably have to
force gcp2prom to be restarted.  This requirement will likely be removed
or at least reduced in a future release.
